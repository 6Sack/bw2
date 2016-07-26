package api

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gopkg.in/vmihailenco/msgpack.v2"

	log "github.com/cihub/seelog"
	"github.com/immesys/bw2/internal/core"
	"github.com/immesys/bw2/objects"
	"github.com/immesys/bw2/objects/advpo"
	"github.com/immesys/bw2/util"
	"github.com/immesys/bw2/util/bwe"
)

type View struct {
	c         *BosswaveClient
	ex        Expression
	metastore map[string]map[string]*advpo.MetadataTuple
	ns        []string
	msmu      sync.RWMutex
	mscond    *sync.Cond
	msloaded  bool
	changecb  []func()
	matchset  []*InterfaceDescription

	subs  []*vsub
	submu sync.Mutex
}

const (
	stateNew = iota
	stateStartSub
	stateSubComplete
	stateStartUnsub
	stateUnsubEnded
	stateToRemove
)

type vsub struct {
	iface    string
	sigslot  string
	isSignal bool
	result   func(m *core.Message)
	actual   []*vsubsub
	v        *View
	mu       sync.Mutex
}

// The expression tree can be used to construct a view using a simple syntax.
// some examples:
/*

If the top object is a list, all the clauses are ANDED together
or {uri:"matchpattern"}
or {uri:{$re:"regexpattern"}}
or {meta:{"key":"value"}}
//or {svc:"servicename"}
//or {iface:"ifacename"}
or {uri:{$or:{$re:..}}}

*/
func _parseURI(t interface{}) (Expression, error) {
	switch t := t.(type) {
	case string:
		return MatchURI(t), nil
	case map[interface{}]interface{}:
		ipat, ok := t["$re"]
		if len(t) > 1 || !ok {
			return nil, fmt.Errorf("unexpected keys in uri filter")
		}
		pat, ok := ipat.(string)
		if !ok {
			return nil, fmt.Errorf("expected string $re pattern")
		}
		return RegexURI(pat), nil
	}
	return nil, fmt.Errorf("unexpected URI structure: %T : %#v", t, t)
}
func _parseMeta(t interface{}) (Expression, error) {
	//fmt.Printf("Parsing meta: %#v", t)
	m, ok := t.(map[interface{}]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected meta structure %T : %#v", t, t)
	}
	rv := []Expression{}
	for ikey, value := range m {
		key, ok1 := ikey.(string)
		valueS, ok2 := value.(string)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("expected map[string]string")
		}
		switch key {
		case "$has":
			rv = append(rv, HasMeta(valueS))
		default:
			rv = append(rv, EqMeta(key, valueS))
		}

	}
	return And(rv...), nil
}
func _parseSvc(t interface{}) (Expression, error) {
	panic("oops")
}
func _parseIface(t interface{}) (Expression, error) {
	panic("oops")
}
func _parseGlobal(t interface{}) (Expression, error) {
	var rt map[string]interface{}
	switch t := t.(type) {
	case []interface{}:
		subex := make([]Expression, len(t))
		var err error
		for i, e := range t {
			subex[i], err = _parseGlobal(e)
			if err != nil {
				return nil, err
			}
		}
		return And(subex...), nil
	case map[interface{}]interface{}:
		rt = make(map[string]interface{})
		for ikey, el := range t {
			key, ok := ikey.(string)
			if !ok {
				return nil, fmt.Errorf("map keys must be strings")
			}
			rt[key] = el
		}
		//do not return
	case map[string]interface{}:
		rt = t
		//do not return
	default:
		return nil, fmt.Errorf("invalid expression structure: %T : %#v", t, t)
	}
	rv := []Expression{}
	for key, el := range rt {
		switch key {
		case "ns":
			slc, ok := el.([]interface{})
			if !ok {
				return nil, fmt.Errorf("operand to 'ns' must be array of strings")
			}
			sslc := []string{}
			for _, se := range slc {
				s, ok := se.(string)
				if !ok {
					return nil, fmt.Errorf("operand to 'ns' must be array of strings")
				}
				sslc = append(sslc, s)
			}
			rv = append(rv, Namespace(sslc...))
		case "uri":
			subex, err := _parseURI(el)
			if err != nil {
				return nil, err
			}
			rv = append(rv, subex)
		case "meta":
			subex, err := _parseMeta(el)
			if err != nil {
				return nil, err
			}
			rv = append(rv, subex)
		case "svc":
			subex, err := _parseSvc(el)
			if err != nil {
				return nil, err
			}
			rv = append(rv, subex)
		case "iface":
			subex, err := _parseIface(el)
			if err != nil {
				return nil, err
			}
			rv = append(rv, subex)
		case "$and":
			sl, ok := el.([]interface{})
			if !ok {
				return nil, fmt.Errorf("operand to $and must be array")
			}
			subex := make([]Expression, len(sl))
			var err error
			for i, e := range sl {
				subex[i], err = _parseGlobal(e)
				if err != nil {
					return nil, err
				}
			}
			rv = append(rv, And(subex...))
		case "$or":
			sl, ok := el.([]interface{})
			if !ok {
				return nil, fmt.Errorf("operand to $or must be array")
			}
			subex := make([]Expression, len(sl))
			var err error
			for i, e := range sl {
				subex[i], err = _parseGlobal(e)
				if err != nil {
					return nil, err
				}
			}
			rv = append(rv, Or(subex...))
		default:
			return nil, fmt.Errorf("unexpected key at this scope: '%s'", key)
		}
	}
	return And(rv...), nil

}
func ExpressionFromTree(t interface{}) (Expression, error) {
	return _parseGlobal(t)
}

// Get the given key for the given fully qualified URI (including ns)
func (v *View) Meta(ruri, key string) (*advpo.MetadataTuple, bool) {
	//TODO going forward, when metadata sub is driven by canonical
	//uri's, it makes sense to check if our canonical uris
	//are sufficient to answer this query

	//This will check uri, and parents (meta is inherited)
	uri, err := v.c.BW().ResolveURI(ruri)
	if err != nil {
		v.fatal(err)
		return nil, false
	}
	parts := strings.Split(uri, "/")
	var val *advpo.MetadataTuple = nil
	set := false
	v.msmu.RLock()
	for i := 1; i <= len(parts); i++ {
		uri := strings.Join(parts[:i], "/")
		m1, ok := v.metastore[uri]
		if ok {
			v, subok := m1[key]
			if subok {
				val = v
				set = true
			}
		}
	}
	v.msmu.RUnlock()
	return val, set
}

// Get all the metadata for the given fully qualified URI (including ns)
func (v *View) AllMeta(ruri string) map[string]*advpo.MetadataTuple {
	uri, err := v.c.BW().ResolveURI(ruri)
	if err != nil {
		v.fatal(err)
		return nil
	}
	parts := strings.Split(uri, "/")
	rv := make(map[string]*advpo.MetadataTuple)
	v.msmu.RLock()
	for i := 1; i <= len(parts); i++ {
		uri := strings.Join(parts[:i], "/")
		m1, ok := v.metastore[uri]
		if ok {
			for kk, vv := range m1 {
				rv[kk] = vv
			}
		}
	}
	v.msmu.RUnlock()
	return rv
}

/*
  (a or b) and (c or d)
*/
func foldAndCanonicalSuffixes(lhs []string, rhsz ...[]string) []string {
	if len(rhsz) == 0 {
		return lhs
	}

	rhs := rhsz[0]
	retv := []string{}
	for _, lv := range lhs {
		for _, rv := range rhs {
			res, ok := util.RestrictBy(lv, rv)
			if ok {
				retv = append(retv, res)
			}
		}
	}
	//Now we need to dedup RV
	// if A restrictBy B == A, then A is redundant and B is superior
	//                   == B, then B is redundant and A is superior
	//  is not equal to either, then both are relevant
	dedup := []string{}
	for out := 0; out < len(retv); out++ {
		for in := 0; in < len(retv); in++ {
			if in == out {
				continue
			}
			res, ok := util.RestrictBy(retv[out], retv[in])
			if ok {
				if res == retv[out] && retv[out] != retv[in] {
					//out is redundant to in, and they are not identical
					//do not add out, as we will add in later
					goto nextOut
				}
				if retv[out] == retv[in] {
					//they are identical (and reduandant) so only add
					//out if it is less than in
					if out > in {
						goto nextOut
					}
				}
			}
		}
		dedup = append(dedup, retv[out])
	nextOut:
	}
	return foldAndCanonicalSuffixes(dedup, rhsz[1:]...)
}

// func Service(name string) Expression {
// 	//uri is .../service/selector/interface/sigslot/endpoint
// 	return MatchURI(fmt.Sprintf("/*/%s/+/+/+/+", name))
// }
// func Interface(name string) Expression {
// 	return RegexURI("^.*/" + name + "$")
// }
func (c *BosswaveClient) NewViewFromBlob(onready func(error, int), blob []byte) {
	var v map[string]interface{}
	err := msgpack.Unmarshal(blob, &v)
	if err != nil {
		onready(err, -1)
		return
	}
	ex, err := ExpressionFromTree(v)
	if err != nil {
		onready(err, -1)
		return
	}
	c.NewView(onready, ex)
}

func (c *BosswaveClient) NewView(onready func(error, int), exz ...Expression) {
	ex := And(exz...)
	nsmap := make(map[string]struct{})
	for _, i := range ex.Namespaces() {
		parts := strings.Split(i, "/")
		nsmap[parts[0]] = struct{}{}
	}
	ns := make([]string, 0, len(nsmap))
	for k, _ := range nsmap {
		ns = append(ns, k)
	}
	rv := &View{
		c:         c,
		ex:        ex,
		metastore: make(map[string]map[string]*advpo.MetadataTuple),
		ns:        ns,
	}
	rv.initMetaView()
	seq := c.registerView(rv)
	go func() {
		rv.waitForMetaView()
		onready(nil, seq)
	}()
}

func (c *BosswaveClient) LookupView(handle int) *View {
	c.viewmu.Lock()
	defer c.viewmu.Unlock()
	v, ok := c.views[handle]
	if ok {
		return v
	}
	return nil
}

func (v *View) waitForMetaView() {
	v.msmu.Lock()
	for !v.msloaded {
		v.mscond.Wait()
	}
	v.msmu.Unlock()
}

func (v *View) checkMatchset() {
	newIfaceList := v.interfacesImpl()
	changed := false
	if len(newIfaceList) != len(v.matchset) {
		changed = true
	}
	if !changed {
		//serious test
		for idx := range newIfaceList {
			if !v.matchset[idx].DeepEquals(newIfaceList[idx]) {
				changed = true
				break
			}
		}
	}

	if changed {
		v.matchset = newIfaceList
		v.checkSubs()
		v.msmu.RLock()
		for _, cb := range v.changecb {
			go cb()
		}
		v.msmu.RUnlock()
	}
}

func (v *View) TearDown() {
	//Release all the assets here
}
func (v *View) fatal(err error) {
	//Sometimes an error can happen deep inside a goroutine, this aborts the view
	//and notifies the client
	panic(err)
}

func (v *View) initMetaView() {
	v.mscond = sync.NewCond(&v.msmu)
	procChange := func(m *core.Message) {
		if m == nil {
			return //we use this for queries too, so we don't know it means
			//end of subscription.
			//v.fatal(fmt.Errorf("subscription ended in view"))
		}
		groups := regexp.MustCompile("^(.*)/!meta/([^/]*)$").FindStringSubmatch(m.Topic)
		if groups == nil {
			fmt.Println("mt is: ", *m.MergedTopic)
			panic("bad re match")
		}
		uri := groups[1]
		key := groups[2]
		v.msmu.Lock()
		map1, ok := v.metastore[uri]
		if !ok {
			map1 = make(map[string]*advpo.MetadataTuple)
			v.metastore[uri] = map1
		}
		var poi advpo.MetadataPayloadObject //sm.GetOnePODF(bw2bind.PODFSMetadata)
		for _, po := range m.PayloadObjects {
			if po.GetPONum() == objects.PONumSMetadata {
				var err error
				poi, err = advpo.LoadMetadataPayloadObject(po.GetPONum(), po.GetContent())
				if err != nil {
					continue
				}
			}
		}
		if poi != nil {
			map1[key] = poi.Value()
		} else {
			delete(map1, key)
		}
		v.msmu.Unlock()
		v.checkMatchset()
	}
	go func() {
		//First subscribe and wait for that to finish
		wg := sync.WaitGroup{}
		wg.Add(len(v.ns))
		for _, n := range v.ns {
			mvk, err := v.c.bw.ResolveKey(n)
			if err != nil {
				v.fatal(err)
				return
			}
			v.c.Subscribe(&SubscribeParams{
				MVK:          mvk,
				URISuffix:    "*/!meta/+",
				ElaboratePAC: PartialElaboration,
				DoVerify:     true,
				AutoChain:    true,
			}, func(err error, id core.UniqueMessageID) {
				wg.Done()
				if err != nil {
					v.fatal(err)
				}
			}, procChange)
		}
		wg.Wait()
		wg = sync.WaitGroup{}
		wg.Add(len(v.ns))
		//Then we query
		for _, n := range v.ns {
			mvk, err := v.c.bw.ResolveKey(n)
			if err != nil {
				v.fatal(err)
				return
			}
			v.c.Query(&QueryParams{
				MVK:          mvk,
				URISuffix:    "*/!meta/+",
				ElaboratePAC: PartialElaboration,
				DoVerify:     true,
				AutoChain:    true,
			}, func(err error) {
				if err != nil {
					v.fatal(err)
				}
			}, func(m *core.Message) {
				if m != nil {
					procChange(m)
				} else {
					wg.Done()
				}
			})
		}
		wg.Wait()

		//Then we mark store as populated
		v.msmu.Lock()
		v.msloaded = true
		v.msmu.Unlock()
		v.mscond.Broadcast()
	}()
}

func (v *View) SubscribeInterface(iface, sigslot string, isSignal bool, reply func(error), result func(m *core.Message)) {
	s := &vsub{iface: iface, sigslot: sigslot, isSignal: isSignal, result: result, v: v}
	v.submu.Lock()
	v.subs = append(v.subs, s)
	v.submu.Unlock()
	v.checkSubs()
	//any errors will go as a fatal view error
	reply(nil)
}

//Check subs is called whenever matchset changes, or subscriptions change
func (v *View) checkSubs() {
	v.submu.Lock()
	for _, s := range v.subs {
		newVss := v.expandSub(s)
		intersection := make(map[*InterfaceDescription]bool)
		tosub := []*InterfaceDescription{}
		toremove := []*vsubsub{}
		//check for new
		for _, id := range newVss {
			//Checking new iterface 'id'
			foundInExisting := false
			for _, oid := range s.actual {
				if oid.id.URI == id.URI {
					foundInExisting = true
					intersection[oid.id] = true
					break
				}
			}
			if !foundInExisting {
				//this is a new iface
				tosub = append(tosub, id)
			}
		}
		//Check for missing
		for _, oid := range s.actual {
			//Skip over entries that we know are in the intersection
			_, donealready := intersection[oid.id]
			if donealready {
				continue
			}
			//Ok this is a sub that needs to be removed
			toremove = append(toremove, oid)
		}
		for _, vss := range toremove {
			s.unsub(vss)
		}
		for _, vss := range tosub {
			s.sub(vss)
		}
	}
	v.submu.Unlock()
}

func (s *vsub) unsub(vss *vsubsub) {
	if vss.state != stateSubComplete {
		log.Criticalf("Unsubscribe, but sub is not complete: %d", vss.state)
		return
	}
	vss.state = stateStartUnsub
	s.v.c.Unsubscribe(vss.subid, func(err error) {
		if err != nil {
			s.v.fatal(err)
		}
	})
}
func (s *vsub) sub(id *InterfaceDescription) {
	vss := &vsubsub{id: id, state: stateStartSub}
	parts := strings.SplitN(id.URI, "/", 2)
	mvk, err := s.v.c.BW().ResolveKey(parts[0])
	if err != nil {
		s.v.fatal(err)
		return
	}
	pfx := "/slot/"
	if s.isSignal {
		pfx = "/signal/"
	}
	suffix := parts[1] + pfx + s.sigslot
	s.v.c.Subscribe(&SubscribeParams{
		MVK:          mvk,
		URISuffix:    suffix,
		ElaboratePAC: PartialElaboration,
		AutoChain:    true,
	}, func(e error, id core.UniqueMessageID) {
		if e != nil {
			s.v.fatal(e)
			return
		}
		s.mu.Lock()
		vss.subid = id
		vss.state = stateSubComplete
		s.actual = append(s.actual, vss)
		s.mu.Unlock()
	}, func(m *core.Message) {
		if m != nil {
			s.result(m)
		} else {
			s.mu.Lock()
			np := s.actual[:0]
			for _, vvv := range s.actual {
				if vvv != vss {
					np = append(np, vvv)
				}
			}
			s.actual = np
			vss.state = stateUnsubEnded
			s.mu.Unlock()

		}
	})
}

type vsubsub struct {
	id    *InterfaceDescription
	state int
	subid core.UniqueMessageID
}

func (v *View) expandSub(s *vsub) []*InterfaceDescription {
	todo := []*InterfaceDescription{}
	for _, viewiface := range v.matchset {
		if viewiface.Interface == s.iface {
			todo = append(todo, viewiface)
		}
	}
	return todo
}

func (v *View) PublishInterface(iface, sigslot string, isSignal bool, poz []objects.PayloadObject, cb func(error)) {
	idz := v.Interfaces()
	pfx := "/slot/"
	if isSignal {
		pfx = "/signal/"
	}
	wg := sync.WaitGroup{}
	todo := []*InterfaceDescription{}
	for _, viewiface := range idz {
		if viewiface.Interface == iface {
			todo = append(todo, viewiface)
			wg.Add(1)
		}
	}
	errc := make(chan error, len(todo)+1)
	for _, viewiface := range todo {
		parts := strings.SplitN(viewiface.URI, "/", 2)
		mvk, err := v.c.BW().ResolveKey(parts[0])
		if err != nil {
			cb(err)
			return
		}
		suffix := parts[1] + pfx + sigslot
		v.c.Publish(&PublishParams{
			MVK:            mvk,
			URISuffix:      suffix,
			AutoChain:      true,
			ElaboratePAC:   PartialElaboration,
			PayloadObjects: poz,
		}, func(e error) {
			if e != nil {
				errc <- e
			}
			wg.Done()
		})
	}
	go func() {
		wg.Wait()
		e := <-errc
		if e != nil {
			cb(bwe.WrapM(bwe.ViewError, "Could not publish", e))
		} else {
			cb(nil)
		}
	}()
}

func (v *View) Interfaces() []*InterfaceDescription {
	return v.matchset
}

func (v *View) interfacesImpl() []*InterfaceDescription {
	v.msmu.RLock()
	found := make(map[string]InterfaceDescription)
	for uri, _ := range v.metastore {
		if v.ex.Matches(uri, v) {
			pat := `^(([^/]+)(/.*)?/(s\.[^/]+)/([^/]+)/(i\.[^/]+)).*$`
			//"^((([^/]+)/(.*)/(s\\.[^/]+)/+)/(i\\.[^/]+)).*$"
			groups := regexp.MustCompile(pat).FindStringSubmatch(uri)
			if groups != nil {
				id := InterfaceDescription{
					URI:       groups[1],
					Interface: groups[6],
					Service:   groups[4],
					Namespace: groups[2],
					Prefix:    groups[5],
					v:         v,
				}
				id.Suffix = strings.TrimPrefix(id.URI, id.Namespace+"/")
				id.Metadata = make(map[string]string)
				for k, v := range v.AllMeta(id.URI) {
					id.Metadata[k] = v.Value
				}
				found[id.URI] = id
			}
		}
	}
	v.msmu.RUnlock()
	rv := []*InterfaceDescription{}
	//TODO maybe we want a real liveness filter here?
	for _, vv := range found {
		if vv.Meta("lastalive") != "" {
			lv := vv
			rv = append(rv, &lv)
		}
	}
	sort.Sort(interfaceSorter(rv))
	return rv
}

type interfaceSorter []*InterfaceDescription

func (is interfaceSorter) Swap(i, j int) {
	is[i], is[j] = is[j], is[i]
}
func (is interfaceSorter) Less(i, j int) bool {
	return strings.Compare(is[i].URI, is[j].URI) < 0
}
func (is interfaceSorter) Len() int {
	return len(is)
}
func (v *View) OnChange(f func()) {
	v.msmu.Lock()
	v.changecb = append(v.changecb, f)
	v.msmu.Unlock()
}

type InterfaceDescription struct {
	URI       string            `msgpack:"uri"`
	Interface string            `msgpack:"iface"`
	Service   string            `msgpack:"svc"`
	Namespace string            `msgpack:"namespace"`
	Prefix    string            `msgpack:"prefix"`
	Suffix    string            `msgpack:"suffix"`
	Metadata  map[string]string `msgpack:"metadata"`
	v         *View
}

func (id *InterfaceDescription) String() string {
	return fmt.Sprintf("ID %s\n", id.URI)
}

//This is not a deep equals, it is only comparing if they refer
//to the same resource (URI)
func (id *InterfaceDescription) Equals(rhs *InterfaceDescription) bool {
	return id.URI == rhs.URI
}
func (id *InterfaceDescription) DeepEquals(rhs *InterfaceDescription) bool {
	if id.URI != rhs.URI {
		return false
	}
	if len(id.Metadata) != len(rhs.Metadata) {
		return false
	}
	for k, idv := range id.Metadata {
		if idv != rhs.Metadata[k] {
			return false
		}
	}
	return true
}
func (id *InterfaceDescription) ToPO() objects.PayloadObject {
	po, err := advpo.CreateMsgPackPayloadObject(objects.PONumInterfaceDescriptor, id)
	if err != nil {
		panic(err)
	}
	return po
}

func (id *InterfaceDescription) Meta(key string) string {
	mdat, ok := id.v.Meta(id.URI, key)
	if !ok {
		return "<unset>"
	}
	return mdat.Value
}

/*
Example use
v := cl.NewView()
q := view.MatchURI(mypattern)
q = q.And(view.MetaEq(key, value))
q = q.And(view.MetaHasKey(key))
q = q.And(view.IsInterface("i.wavelet"))
q = q.And(view.IsService("s.thingy"))
v = v.And(view.MatchURI(myurl, mypattern))

can assume all interfaces are persisted up to .../i.foo/
when you subscribe,
*/

type Expression interface {
	//Return a list of all namespaces that this expression would make
	//you want to operate on
	Namespaces() []string

	//Given a complete resource name, does this expression
	//permit it to be included in the view
	Matches(uri string, v *View) bool

	//Return a list of all URIs(sans namespaces) that are sufficient
	//to evaluate this expression (minimum subscription set). Does not
	//include metadata
	CanonicalSuffixes() []string
	//Given a partial resource name (prefix) does this expression
	//possibly permit it to be included in the view. Yes means maybe
	//no means no.
	MightMatch(uri string, v *View) bool
}
