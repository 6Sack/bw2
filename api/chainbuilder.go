package api

import (
	"bytes"
	"container/list"
	"errors"
	"fmt"
	"strings"

	log "github.com/cihub/seelog"
	"github.com/immesys/bw2/crypto"
	"github.com/immesys/bw2/objects"
	"github.com/immesys/bw2/util"
)

type CBCache interface {
	Lookup(ck CacheKey) []*objects.DChain
}
type CacheKey struct {
	uri    string
	perms  string
	target [32]byte
	nsvk   [32]byte
}
type ChainBuilder struct {
	cl     *BosswaveClient
	status chan string
	uri    string
	perms  string
	target []byte
	//	fulluri   []byte
	nsvk      []byte
	urisuffix string
	desperms  *objects.AccessDOTPermissionSet
}

func (c *BosswaveClient) LookupChain(ck *CacheKey) {

}

type scenario struct {
	chain  []*objects.DOT
	suffix string
}

func (s *scenario) TTL() int {
	ttl := 256
	for _, d := range s.chain {
		ttl = ttl - 1
		if d.GetTTL() < ttl {
			ttl = d.GetTTL()
		}
	}
	return ttl
}
func (s *scenario) Clone() *scenario {
	cc := make([]*objects.DOT, len(s.chain))
	copy(cc, s.chain)
	return &scenario{chain: cc}
}
func (s *scenario) String() string {
	rv := "["
	for _, d := range s.chain {
		rv += crypto.FmtKey(d.GetHash()) + ","
	}
	return rv + "]"
}
func NewScenario(d *objects.DOT) *scenario {
	return &scenario{chain: []*objects.DOT{d}, suffix: d.GetAccessURISuffix()}
}
func (s *scenario) AddAndClone(d *objects.DOT) (*scenario, bool) {
	cc := make([]*objects.DOT, len(s.chain)+1)
	copy(cc, s.chain)
	cc[len(s.chain)] = d
	nuri, okay := util.RestrictBy(s.suffix, d.GetAccessURISuffix())
	if !okay {
		return nil, false
	}
	rv := &scenario{chain: cc, suffix: nuri}
	if rv.TTL() < 0 {
		return nil, false
	}
	return rv, true
}

func (s *scenario) GetTerminalVK() []byte {
	return s.chain[len(s.chain)-1].GetReceiverVK()
}

func (s *scenario) ToChain() *objects.DChain {
	rv, err := objects.CreateDChain(true, s.chain...)
	if err != nil {
		panic(err)
	}
	return rv
}
func NewChainBuilder(cl *BosswaveClient, uri, perms string, target []byte, status chan string) *ChainBuilder {
	rv := ChainBuilder{cl: cl,
		uri:      uri,
		target:   target,
		perms:    perms,
		status:   status,
		desperms: objects.GetADPSFromPermString(perms)}
	if rv.desperms == nil {
		status <- "Bad permissions"
		return nil
	}
	uriparts := strings.SplitN(uri, "/", 2)
	nsvk, err := cl.BW().ResolveKey(uriparts[0])
	if err != nil {
		panic("need to fix this")
	}
	rv.urisuffix = uriparts[1]
	rv.nsvk = nsvk
	return &rv
}

func (b *ChainBuilder) dotUseful(d *objects.DOT) bool {
	adps := d.GetPermissionSet()
	if !bytes.Equal(d.GetAccessURIMVK(), b.nsvk) {
		b.status <- fmt.Sprintf("rejecting DOT(%s) - incorrect namespace", crypto.FmtHash(d.GetHash()))
		return false
	}
	if !b.desperms.IsSubsetOf(adps) {
		b.status <- fmt.Sprintf("rejecting DOT(%s) - insufficient ADPS", crypto.FmtHash(d.GetHash()))
		return false
	}
	nu, ok := util.RestrictBy(b.urisuffix, d.GetAccessURISuffix())
	if !ok || nu != b.urisuffix {
		b.status <- fmt.Sprintf("rejecting DOT(%s) - DOT URI is too restrictive", crypto.FmtHash(d.GetHash()))
		return false
	}
	return true
}

func (b *ChainBuilder) getOptions(from []byte) []*objects.DOT {
	dlz, err := b.cl.BW().GetDOTsFrom(from)
	if err != nil {
		panic(err)
	}
	rv := []*objects.DOT{}
	for _, dl := range dlz {
		if dl.S != StateValid {
			b.status <- fmt.Sprintf("rejecting DOT(%s) - Status is %d", crypto.FmtHash(dl.D.GetHash()), dl.S)
			continue
		}
		if b.dotUseful(dl.D) {
			b.status <- "possible edge DOT: " + crypto.FmtHash(dl.D.GetHash())
			rv = append(rv, dl.D)
		}
	}
	return rv
}

func (b *ChainBuilder) Build() ([]*objects.DChain, error) {
	ck := CacheKey{
		uri:   b.uri,
		perms: b.perms,
	}
	copy(ck.target[:], b.target)
	copy(ck.nsvk[:], b.nsvk)
	cached := b.cl.bw.LookupChain(&ck)
	if cached != nil {
		log.Infof("chain build cache hit")
		return cached, nil
	} else {
		log.Infof("chain build cache miss")
	}
	parts := strings.SplitN(b.uri, "/", 2)
	if len(parts) != 2 {
		return nil, errors.New("Invalid URI")
	}
	valid, _, _, _, _ := util.AnalyzeSuffix(parts[1])
	if !valid {
		return nil, errors.New("Invalid URI")
	}
	mvk, err := b.cl.BW().ResolveKey(parts[0])
	if err != nil {
		return nil, err
	}
	validscenarios := list.New()
	evals := list.New()
	b.status <- "populating initial options"
	b.status <- "looking for DOTs from " + crypto.FmtKey(mvk)
	initial := b.getOptions(mvk)
	for _, dt := range initial {
		s := NewScenario(dt)
		if bytes.Equal(s.GetTerminalVK(), b.target) || bytes.Equal(s.GetTerminalVK(), util.EverybodySlice) {
			b.status <- "found valid scenario"
			validscenarios.PushBack(s)
		} else {
			b.status <- "adding scenario: " + s.String()
			evals.PushBack(s)
		}
	}
	for evals.Front() != nil {
		le := evals.Front()
		s := le.Value.(*scenario)
		endsat := s.GetTerminalVK()
		opts := b.getOptions(endsat)
		for _, dt := range opts {
			newscenario, okay := s.AddAndClone(dt)
			if !okay {
				continue
			}
			if bytes.Equal(newscenario.GetTerminalVK(), b.target) || bytes.Equal(newscenario.GetTerminalVK(), util.EverybodySlice) {
				b.status <- "graph walk found a valid scenario!"
				validscenarios.PushBack(newscenario)
			} else {
				evals.PushBack(newscenario)
			}
		}
		evals.Remove(le)
	}
	seen := make(map[string]bool)
	rv := make([]*objects.DChain, 0, validscenarios.Len())
	e := validscenarios.Front()
	for e != nil {
		chn := e.Value.(*scenario).ToChain()
		k := crypto.FmtHash(chn.GetChainHash())
		_, ok := seen[k]
		if !ok {
			rv = append(rv, chn)
		}
		e = e.Next()
	}
	b.status <- "chain build operation complete"
	close(b.status)
	b.cl.bw.CacheChain(&ck, rv)
	return rv, nil
}
