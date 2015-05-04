package objects

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/immesys/bw2/internal/crypto"
)

//RoutingObject is the interface that is common among all objects that
//appear in the routing object block
type RoutingObject interface {
	GetRONum() int
	GetContent() []byte
}

type sigState int8

const (
	sigUnchecked = iota
	sigValid
	sigInvalid
)

//ROAccessDChainHash is the constant for an Access DChain hash
const ROAccessDChainHash = 0x01

//ROPermissionDChainHash is the constant for a Permission DChain hash
const ROPermissionDChainHash = 0x11

//ROAccessDChain is the constant for a full Access DChain
const ROAccessDChain = 0x02

//ROPermissionDChain is the constant for a full Permission DChain
const ROPermissionDChain = 0x12

//ROAccessDOT is the constant for an Access DOT
const ROAccessDOT = 0x20

//ROPermissionDOT is the constant for a Permission DOT
const ROPermissionDOT = 0x21

//RoutingObjectConstruct allows you to map a ROnum into a constructor that takes a
//binary representation and returns a Routing Object
var RoutingObjectConstructor = map[int]func(ronum int, content []byte) (RoutingObject, error){
	0x01: NewDChain,
	0x11: NewDChain,
	0x02: NewDChain,
	0x12: NewDChain,
	0x20: NewDOT,
}

// DChain is a list of DOT hashes
type DChain struct {
	dothashes  []byte
	chainhash  []byte
	dots       []DOT
	isAccess   bool
	ronum      int
	elaborated bool
}

//NewDChain deserialises a DChain from a byte array
func NewDChain(ronum int, content []byte) (rv RoutingObject, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = NewObjectError(ronum, "Bad DChain")
			rv = nil
		}
	}()
	ro := DChain{ronum: ronum}
	switch ronum {
	case ROAccessDChain, ROPermissionDChain:
		if len(content)%32 != 0 || len(content) == 0 {
			return nil, NewObjectError(ronum, "Wrong content length")
		}
		ro.dothashes = content
		sum := sha256.Sum256(content)
		ro.chainhash = sum[:]
		ro.isAccess = ronum == 0x02
		ro.elaborated = true
		ro.dots = make([]DOT, len(content)/32)
		return &ro, nil
	case ROAccessDChainHash, ROPermissionDChainHash:
		if len(content) != 32 {
			return nil, NewObjectError(ronum, "Wrong content length")
		}
		ro.chainhash = content
		ro.isAccess = ronum == 0x01
		return &ro, nil
	default:
		panic("Should not have reached here")
	}
}

//NumHashes returns the length of the chain
func (ro *DChain) NumHashes() int {
	if ro.elaborated {
		return len(ro.dots)
	}
	panic("DChain not elaborated")
}

//GetHash returns the hash at the specific index
func (ro *DChain) GetHash(num int) []byte {
	return ro.dothashes[num*32 : (num+1)*32]
}

//GetRONum returns the RONum for this object
func (ro *DChain) GetRONum() int {
	return ro.ronum
}

//GetContent returns the serialised content for this object
func (ro *DChain) GetContent() []byte {
	switch ro.ronum {
	case ROAccessDChain, ROPermissionDChain:
		return ro.dothashes
	case ROAccessDChainHash, ROPermissionDChainHash:
		return ro.chainhash
	default:
		panic("Invalid RONUM")
	}
}

//PublishLimits is an option found in an AccessDOT that governs
//the resources that may be used by messages authorised via the DOT
type PublishLimits struct {
	TxLimit    int64
	StoreLimit int64
	Retain     int
}

func (p *PublishLimits) toBytes() []byte {
	rv := make([]byte, 17)
	binary.LittleEndian.PutUint64(rv, uint64(p.TxLimit))
	binary.LittleEndian.PutUint64(rv, uint64(p.StoreLimit))
	rv[16] = byte(p.Retain)
	return rv
}

//DOT is a declaration of trust. This is a shared object that implements
//both an access dot and a permission dot
type DOT struct {
	content    []byte
	hash       []byte
	giverVK    []byte //VK
	receiverVK []byte
	expires    *time.Time
	created    *time.Time
	revokers   [][]byte
	contact    string
	comment    string
	signature  []byte
	isAccess   bool
	ttl        int
	sigok      sigState

	//Only for ACCESS dot
	mVK            []byte
	uriSuffix      string
	uri            string
	pubLim         *PublishLimits
	canPublish     bool
	canConsume     bool
	canConsumePlus bool
	canConsumeStar bool
	canTap         bool
	canTapPlus     bool
	canTapStar     bool
	canList        bool

	//Only for Permission dot
	kv map[string]string
}

//NewDOT constructs a DOT from its packed form
func NewDOT(ronum int, content []byte) (rv RoutingObject, err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println(r)
			debug.PrintStack()
			err = NewObjectError(ronum, "Bad DoT")
			rv = nil
		}
	}()

	idx := 0
	ro := DOT{
		giverVK:    content[0:32],
		receiverVK: content[32:64],
		ttl:        int(content[64]),
		revokers:   make([][]byte, 0),
		kv:         make(map[string]string),
		content:    content,
	}

	idx = 65
	for {
		switch content[idx] {
		case 0x01: //Publish limits
			if content[idx+1] != 17 {
				return nil, NewObjectError(ronum, "Invalid publim in DoT")
			}
			idx += 2
			ro.pubLim = &PublishLimits{
				TxLimit:    int64(binary.LittleEndian.Uint64(content[idx:])),
				StoreLimit: int64(binary.LittleEndian.Uint64(content[idx+8:])),
				Retain:     int(content[idx+16]),
			}
			idx += 17
		case 0x02: //Creation date
			if content[idx+1] != 8 {
				return nil, NewObjectError(ronum, "Invalid creation date in DoT")
			}
			idx += 2
			t := time.Unix(0, int64(binary.LittleEndian.Uint64(content[idx:])*1000000))
			ro.created = &t
			idx += 8
		case 0x03: //Expiry date
			if content[idx+1] != 8 {
				return nil, NewObjectError(ronum, "Invalid expiry date in DoT")
			}
			idx += 2
			t := time.Unix(0, int64(binary.LittleEndian.Uint64(content[idx:])*1000000))
			ro.expires = &t
			idx += 8
		case 0x04: //Delegated revoker
			if content[idx+1] != 8 {
				return nil, NewObjectError(ronum, "Invalid delegated revoker in DoT")
			}
			idx += 2
			ro.revokers = append(ro.revokers, content[idx:idx+32])
			idx += 32
		case 0x05: //contact
			ln := int(content[idx+1])
			ro.contact = string(content[idx+2 : idx+2+ln])
			idx += 2 + ln
		case 0x06: //Comment
			ln := int(content[idx+1])
			ro.comment = string(content[idx+2 : idx+2+ln])
			idx += 2 + ln
		case 0x00: //End
			idx++
			goto done
		default: //Skip unknown header
			fmt.Println("Unknown DoT header type: ", content[idx])
			idx += int(content[idx+1]) + 1

		}
	}
done:
	if ronum == ROAccessDOT {
		ro.isAccess = true
		perm := binary.LittleEndian.Uint16(content[idx:])
		idx += 2
		if perm&0x0001 != 0 {
			ro.canConsume = true
		}
		if perm&0x0002 != 0 {
			ro.canConsumePlus = true
			ro.canConsume = true
		}
		if perm&0x0004 != 0 {
			ro.canConsumeStar = true
			ro.canConsumePlus = true
			ro.canConsume = true
		}
		if perm&0x0008 != 0 {
			ro.canTap = true
		}
		if perm&0x0010 != 0 {
			ro.canTapPlus = true
			ro.canTap = true
		}
		if perm&0x0020 != 0 {
			ro.canTapStar = true
			ro.canTapPlus = true
			ro.canTap = true
		}
		if perm&0x0040 != 0 {
			ro.canPublish = true
		}
		if perm&0x0080 != 0 {
			ro.canList = true
		}

		ro.mVK = content[idx : idx+32]
		idx += 32
		ln := int(binary.LittleEndian.Uint16(content[idx:]))
		idx += 2
		ro.uriSuffix = string(content[idx : idx+ln])
		ro.uri = base64.URLEncoding.EncodeToString(ro.mVK) + "/" + ro.uriSuffix
		idx += ln
	} else if ronum == ROPermissionDOT {
		//Parse Key value
		for {
			keylen := int(content[idx])
			if keylen == 0 {
				idx++
				break
			}
			key := string(content[idx+1 : idx+1+keylen])
			idx += 1 + keylen
			valLen := int(binary.LittleEndian.Uint16(content[idx:]))
			val := string(content[idx+2 : idx+2+valLen])
			idx += 2 + valLen
			ro.kv[key] = val
		}
	} else {
		return nil, NewObjectError(ronum, "Unknown RONum")
	}
	hash := sha256.Sum256(content[0:idx])
	ro.hash = hash[:]
	ro.signature = content[idx : idx+64]
	return &ro, nil
}

//SigValid returns if the DOT's signature is valid. This only checks
//the signature on the first call, so the content must not change
//after encoding for this to be valid
func (ro *DOT) SigValid() bool {
	if ro.sigok == sigValid {
		return true
	} else if ro.sigok == sigInvalid {
		return false
	}
	if len(ro.signature) != 64 || len(ro.content) == 0 {
		panic("DOT in invalid state")
	}
	ok := crypto.VerifyBlob(ro.giverVK, ro.signature, ro.content[:len(ro.content)-64])
	if ok {
		ro.sigok = sigValid
		return true
	}
	ro.sigok = sigInvalid
	return false
}

//SetCanConsume sets the consume privileges on an access dot
func (ro *DOT) SetCanConsume(normal bool, plus bool, star bool) {
	if !ro.isAccess {
		panic("Not an access DOT")
	}
	plus = plus || star
	normal = normal || plus
	ro.canConsume = normal
	ro.canConsumePlus = plus
	ro.canConsumeStar = star
}

//SetCreation sets the creation timestamp on the DOT
func (ro *DOT) SetCreation(time time.Time) {
	ro.created = &time
}

//SetCreationToNow sets the creation timestamp to the current time
func (ro *DOT) SetCreationToNow() {
	t := time.Now().UnixNano()
	t /= 1000000
	t *= 1000000
	to := time.Unix(0, t)
	ro.created = &to
}

//SetExpiry sets the expiry time to the given time
func (ro *DOT) SetExpiry(time time.Time) {
	ro.expires = &time
}

//SetExpireFromNow is a convenience function that sets the creation time
//to now, and sets the expiry to the given delta from the creation time
func (ro *DOT) SetExpireFromNow(delta time.Duration) {
	ro.SetCreationToNow()
	e := ro.created.Add(delta)
	ro.expires = &e
}

//SetCanTap sets the tap capability on an access dot
func (ro *DOT) SetCanTap(normal bool, plus bool, star bool) {
	if !ro.isAccess {
		panic("Not an access DOT")
	}
	plus = plus || star
	normal = normal || plus
	ro.canTap = normal
	ro.canTapPlus = plus
	ro.canTapStar = star
}

//SetCanPublish sets the publish capability on an access DOT
func (ro *DOT) SetCanPublish(value bool) {
	if !ro.isAccess {
		panic("Not an access DOT")
	}
	ro.canPublish = value
}

//SetCanList sets the list capability on an access DOT
func (ro *DOT) SetCanList(value bool) {
	if !ro.isAccess {
		panic("Not an access DOT")
	}
	ro.canList = value
}

//CreateDOT is used to create a DOT from scratch. The DOT is incomplete until
//Encode() is called later.
func CreateDOT(isAccess bool, giverVK []byte, receiverVK []byte) *DOT {
	rv := DOT{isAccess: isAccess, giverVK: giverVK, receiverVK: receiverVK, kv: make(map[string]string), revokers: make([][]byte, 0)}
	return &rv
}

//GetRONum returns the ronum of the dot
func (ro *DOT) GetRONum() int {
	if ro.isAccess {
		return ROAccessDOT
	}
	return ROPermissionDOT
}

//GetContent returns the binary representation of the DOT if Encode has been called
func (ro *DOT) GetContent() []byte {
	return ro.content
}

//SetAccessURI sets the URI of an Access DOT
func (ro *DOT) SetAccessURI(mvk []byte, suffix string) {
	if !ro.isAccess {
		panic("Should be an access DOT")
	}
	ro.mVK = mvk
	ro.uriSuffix = suffix
	ro.uri = base64.URLEncoding.EncodeToString(ro.mVK) + "/" + ro.uriSuffix
}

//SetPermission sets the given key in a Permission DOT's table
func (ro *DOT) SetPermission(key string, value string) {
	if ro.isAccess {
		panic("Should be a permission DOT")
	}
	if len(key) > 255 || len(value) > 65535 {
		panic("Permission is too big")
	}
	ro.kv[key] = value
}

//GetTTL gets the TTL of a DOT
func (ro *DOT) GetTTL() int {
	return ro.ttl
}

//SetTTL sets the TTL of a dot
func (ro *DOT) SetTTL(v int) {
	if v < 0 || v > 255 {
		panic("Bad TTL")
	}
	ro.ttl = v
}

//GetPermString gets the human readable permission string for an access dot
func (ro *DOT) GetPermString() string {
	if !ro.isAccess {
		panic("Should be an access DOT")
	}
	rv := ""
	if ro.canConsumeStar {
		rv += "C*"
	} else if ro.canConsumePlus {
		rv += "C+"
	} else if ro.canConsume {
		rv += "C"
	}
	if ro.canTapStar {
		rv += "T*"
	} else if ro.canTapPlus {
		rv += "T+"
	} else if ro.canTap {
		rv += "T"
	}
	if ro.canPublish {
		rv += "P"
	}
	if ro.canList {
		rv += "L"
	}
	return rv
}

//String returns a string representation of the DOT
func (ro *DOT) String() string {
	rv := "[DOT]\n"
	if ro.isAccess {
		rv += "ACCESS " + ro.GetPermString() + "\n"
	} else {
		rv += "PERMISSION\n"
	}
	rv += "Hash: " + crypto.FmtHash(ro.hash) + "\n"
	rv += "From VK: " + crypto.FmtKey(ro.giverVK) + "\n"
	rv += "To VK  : " + crypto.FmtKey(ro.receiverVK) + "\n"
	if ro.created != nil {
		rv += "Created: " + ro.created.String() + "\n"
	}
	if ro.expires != nil {
		rv += "Expires: " + ro.expires.String()
	}
	if ro.pubLim != nil {
		rv += "PubLim: store(" + string(ro.pubLim.StoreLimit) + ") tx(" + string(ro.pubLim.TxLimit) + ") p(" + string(ro.pubLim.Retain) + ")\n"
	}
	return rv
}

//Encode will work out the content of the DOT based on the fields
//that have been set, and sign it with the given sk (must match the vk)
func (ro *DOT) Encode(sk []byte) {
	buf := make([]byte, 65, 256)
	copy(buf, ro.giverVK)
	copy(buf[32:], ro.receiverVK)
	buf[64] = byte(ro.ttl)
	//max = 64
	if ro.pubLim != nil {
		buf = append(buf, 0x01, 17)
		buf = append(buf, ro.pubLim.toBytes()...)
	}
	//max = 83
	if ro.created != nil {
		buf = append(buf, 0x02, 8)
		tmp := make([]byte, 8)
		binary.LittleEndian.PutUint64(tmp, uint64(ro.created.UnixNano()/1000000))
		buf = append(buf, tmp...)
	}
	//max = 93
	if ro.expires != nil {
		buf = append(buf, 0x03, 8)
		tmp := make([]byte, 8)
		binary.LittleEndian.PutUint64(tmp, uint64(ro.expires.UnixNano()/1000000))
		buf = append(buf, tmp...)
	}
	//max = 103
	for _, dr := range ro.revokers {
		buf = append(buf, 0x04, 32)
		buf = append(buf, dr...)
	}
	if ro.contact != "" {
		if len(ro.contact) > 255 {
			ro.contact = ro.contact[:255]
		}
		buf = append(buf, 0x05, byte(len(ro.contact)))
		buf = append(buf, []byte(ro.contact)...)
	}
	if ro.comment != "" {
		if len(ro.comment) > 255 {
			ro.comment = ro.comment[:255]
		}
		buf = append(buf, 0x06, byte(len(ro.comment)))
		buf = append(buf, []byte(ro.comment)...)
	}
	buf = append(buf, 0x00)
	if ro.isAccess {
		perm := 0
		if ro.canConsume {
			perm |= 0x01
		}
		if ro.canConsumePlus {
			perm |= 0x03
		}
		if ro.canConsumeStar {
			perm |= 0x07
		}
		if ro.canTap {
			perm |= 0x08
		}
		if ro.canTapPlus {
			perm |= 0x18
		}
		if ro.canTapStar {
			perm |= 0x38
		}
		if ro.canPublish {
			perm |= 0x40
		}
		if ro.canList {
			perm |= 0x80
		}
		buf = append(buf, byte(perm), 0x00)
		buf = append(buf, ro.mVK...)
		tmp := make([]byte, 2)
		binary.LittleEndian.PutUint16(tmp, uint16(len(ro.uriSuffix)))
		buf = append(buf, tmp...)
		buf = append(buf, []byte(ro.uriSuffix)...)
	} else {
		tmp := make([]byte, 2)
		for key, value := range ro.kv {
			buf = append(buf, byte(len(key)))
			buf = append(buf, []byte(key)...)
			binary.LittleEndian.PutUint16(tmp, uint16(len(value)))
			buf = append(buf, tmp...)
			buf = append(buf, []byte(value)...)
		}
		buf = append(buf, 0)
	}
	hash := sha256.Sum256(buf)
	ro.hash = hash[:]
	sig := make([]byte, 64)
	crypto.SignBlob(sk, ro.giverVK, sig, buf)
	buf = append(buf, sig...)
	ro.content = buf
	ro.signature = sig

}
