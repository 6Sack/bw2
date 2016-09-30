// This file is part of BOSSWAVE.
//
// BOSSWAVE is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// BOSSWAVE is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with BOSSWAVE.  If not, see <http://www.gnu.org/licenses/>.
//
// Copyright © 2015 Michael Andersen <m.andersen@cs.berkeley.edu>

package api

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/cihub/seelog"
	"github.com/immesys/bw2/crypto"
	"github.com/immesys/bw2/internal/core"
	"github.com/immesys/bw2/util/bwe"
)

type PeerClient struct {
	seqno      uint64
	conn       net.Conn
	txmtx      sync.Mutex
	replyCB    map[uint64]func(*nativeFrame)
	expectedVK []byte
	target     string
	bwcl       *BosswaveClient
	asublock   sync.Mutex
	activesubs map[uint64]*core.Message
}

func (cl *PeerClient) reconnectPeer() error {
	roots := x509.NewCertPool()
	conn, err := tls.Dial("tcp", cl.target, &tls.Config{
		InsecureSkipVerify: true,
		RootCAs:            roots,
	})
	if err != nil {
		return err
	}
	cs := conn.ConnectionState()
	if len(cs.PeerCertificates) != 1 {
		log.Criticalf("peer connection weird response")
		return errors.New("Wrong certificates")
	}
	proof := make([]byte, 96)
	_, err = io.ReadFull(conn, proof)
	if err != nil {
		return errors.New("failed to read proof: " + err.Error())
	}
	proofOK := crypto.VerifyBlob(proof[:32], proof[32:], cs.PeerCertificates[0].Signature)
	if !proofOK {
		return errors.New("peer verification failed")
	}
	if !bytes.Equal(proof[:32], cl.expectedVK) {
		return errors.New("peer has a different VK")
	}
	cl.txmtx.Lock()
	cl.conn = conn
	cl.txmtx.Unlock()
	return nil
}

func (cl *BosswaveClient) ConnectToPeer(vk []byte, target string) (*PeerClient, error) {

	rv := PeerClient{
		conn:       nil,
		replyCB:    make(map[uint64]func(*nativeFrame)),
		target:     target,
		bwcl:       cl,
		expectedVK: vk,
		activesubs: make(map[uint64]*core.Message),
	}
	err := rv.reconnectPeer()
	if err != nil {
		return nil, err
	}
	go rv.rxloop()
	return &rv, nil
}

func (pc *PeerClient) Destroy() {
	//We don't care about our subs
	pc.asublock.Lock()
	pc.activesubs = make(map[uint64]*core.Message)
	pc.asublock.Unlock()
	pc.conn.Close()
}
func (pc *PeerClient) GetTarget() string {
	return pc.target
}
func (pc *PeerClient) GetRemoteVK() []byte {
	return pc.expectedVK
}
func (pc *PeerClient) regenSubs() {
	pc.asublock.Lock()
	defer pc.asublock.Unlock()
	for seqno, msg := range pc.activesubs {
		nf := nativeFrame{
			cmd:   nCmdMessage,
			body:  msg.Encoded,
			seqno: seqno,
		}
		pc.txmtx.Lock()
		cb := pc.replyCB[seqno]
		//We don't really want result or status frames
		filter := func(f *nativeFrame) {
			switch f.cmd {
			case nCmdResult:
				fallthrough
			case nCmdEnd:
				cb(f)
			}
		}
		pc.txmtx.Unlock()
		pc.transact(&nf, filter)
	}
}
func (pc *PeerClient) rxloop() {
	hdr := make([]byte, 17)
	for {
		_, err := io.ReadFull(pc.conn, hdr)
		if err != nil {
			log.Infof("PEER CONNECTION to %s: %s", pc.target, err)
			pc.conn.Close()
			pc.txmtx.Lock()
			cbz := pc.replyCB
			for _, e := range cbz {
				go e(nil)
			}
			pc.txmtx.Unlock()
			for {
				log.Infof("Attempting to reconnect to peer: %s", pc.target)
				err := pc.reconnectPeer()
				if err == nil {
					log.Infof("Peer reconnected: %s", pc.target)
					pc.regenSubs()
					break
				} else {
					time.Sleep(5 * time.Second)
				}
			}
			continue
		}
		ln := binary.LittleEndian.Uint64(hdr)
		seqno := binary.LittleEndian.Uint64(hdr[8:])
		cmd := hdr[16]
		body := make([]byte, ln)
		_, err = io.ReadFull(pc.conn, body)
		if err != nil {
			log.Info("peer client: ", err)
			continue
		}
		fr := nativeFrame{
			length: ln,
			seqno:  seqno,
			cmd:    cmd,
			body:   body,
		}
		//fmt.Printf("dispatching peer frame %x to %d\n", cmd, seqno)
		pc.txmtx.Lock()
		cb := pc.replyCB[seqno]
		pc.txmtx.Unlock()
		cb(&fr)
	}
}
func (pc *PeerClient) getSeqno() uint64 {
	return atomic.AddUint64(&pc.seqno, 1)
}
func (pc *PeerClient) removeCB(seqno uint64) {
	pc.txmtx.Lock()
	delete(pc.replyCB, seqno)
	pc.txmtx.Unlock()
}
func (pc *PeerClient) transact(f *nativeFrame, onRX func(f *nativeFrame)) {
	tmphdr := make([]byte, 17)
	binary.LittleEndian.PutUint64(tmphdr, uint64(len(f.body)))
	binary.LittleEndian.PutUint64(tmphdr[8:], f.seqno)
	tmphdr[16] = byte(f.cmd)
	pc.txmtx.Lock()
	pc.replyCB[f.seqno] = onRX
	defer pc.txmtx.Unlock()
	_, err := pc.conn.Write(tmphdr)
	if err != nil {
		log.Info("peer write error: ", err.Error())
		pc.conn.Close()
		go onRX(nil)
		return
	}
	_, err = pc.conn.Write(f.body)
	if err != nil {
		log.Info("peer write error: ", err.Error())
		pc.conn.Close()
		go onRX(nil)
	}
}
func (pc *PeerClient) PublishPersist(m *core.Message, actionCB func(err error)) {
	nf := nativeFrame{
		cmd:   nCmdMessage,
		body:  m.Encoded,
		seqno: pc.getSeqno(),
	}
	pc.transact(&nf, func(f *nativeFrame) {
		defer pc.removeCB(nf.seqno)
		if f == nil {
			actionCB(bwe.M(bwe.PeerError, "Peer disconnected"))
			return
		}
		if len(f.body) < 2 {
			actionCB(bwe.M(bwe.PeerError, "short response frame"))
			return
		}
		code := int(binary.LittleEndian.Uint16(f.body))
		msg := string(f.body[2:])
		if code != bwe.Okay {
			actionCB(bwe.M(code, msg))
		} else {
			actionCB(nil)
		}
		return
	})
}

func (pc *PeerClient) Subscribe(m *core.Message,
	actionCB func(err error, id core.UniqueMessageID),
	messageCB func(m *core.Message)) {
	nf := nativeFrame{
		cmd:   nCmdMessage,
		body:  m.Encoded,
		seqno: pc.getSeqno(),
	}
	pc.transact(&nf, func(f *nativeFrame) {
		if f == nil {
			//Peer error, on a subscribe it will just get regenned
			return
		}
		switch f.cmd {
		case nCmdRStatus:
			fallthrough
		case nCmdRSub:
			log.Infof("Got subscribe status response")
			if len(f.body) < 2 {
				actionCB(bwe.M(bwe.PeerError, "short response frame"), core.UniqueMessageID{})
				return
			}
			code := int(binary.LittleEndian.Uint16(f.body))
			if code != bwe.Okay {
				actionCB(bwe.M(code, string(f.body[2:])), core.UniqueMessageID{})
			} else {
				mid := binary.LittleEndian.Uint64(f.body[2:])
				sig := binary.LittleEndian.Uint64(f.body[10:])
				umid := core.UniqueMessageID{Mid: mid, Sig: sig}
				pc.asublock.Lock()
				pc.activesubs[nf.seqno] = m
				pc.asublock.Unlock()
				actionCB(nil, umid)
			}
			return
		case nCmdResult:
			//log.Infof("Got subscribe message response")
			nm, err := core.LoadMessage(f.body)
			if err != nil {
				log.Info("dropping incoming subscription result (malformed message)")
				return
			}
			err = nm.Verify(pc.bwcl.BW())
			if err != nil {
				log.Infof("dropping incoming subscription result on uri=%s (failed local validation %s)", nm.Topic, err.Error())
				return
			}
			messageCB(nm)
			return
		case nCmdEnd:
			//This will be signalled when we unsubscribe
			pc.asublock.Lock()
			delete(pc.activesubs, nf.seqno)
			pc.asublock.Unlock()
			messageCB(nil)
			pc.removeCB(nf.seqno)
		}
	})
}
func (pc *PeerClient) Unsubscribe(m *core.Message, actionCB func(err error)) {
	nf := nativeFrame{
		cmd:   nCmdMessage,
		body:  m.Encoded,
		seqno: pc.getSeqno(),
	}
	pc.transact(&nf, func(f *nativeFrame) {
		defer pc.removeCB(nf.seqno)
		if len(f.body) < 2 {
			actionCB(bwe.M(bwe.PeerError, "short response frame"))
			return
		}
		code := int(binary.LittleEndian.Uint16(f.body))
		msg := string(f.body[2:])
		if code != bwe.Okay {
			actionCB(bwe.M(code, msg))
		} else {
			actionCB(nil)
		}
		return
	})
}
func (pc *PeerClient) List(m *core.Message,
	actionCB func(err error),
	resultCB func(uri string, ok bool)) {
	nf := nativeFrame{
		cmd:   nCmdMessage,
		body:  m.Encoded,
		seqno: pc.getSeqno(),
	}
	pc.transact(&nf, func(f *nativeFrame) {
		switch f.cmd {
		case nCmdRStatus:
			if len(f.body) < 2 {
				actionCB(bwe.M(bwe.PeerError, "short response frame"))
				return
			}
			code := int(binary.LittleEndian.Uint16(f.body))
			if code != bwe.Okay {
				actionCB(bwe.M(code, string(f.body[2:])))
			} else {
				actionCB(nil)
			}
			return
		case nCmdResult:
			resultCB(string(f.body), true)
			return
		case nCmdEnd:
			//This will be signalled when we unsubscribe
			resultCB("", false)
			pc.removeCB(nf.seqno)
		}
	})
}

func (pc *PeerClient) Query(m *core.Message,
	actionCB func(err error),
	resultCB func(m *core.Message)) {
	nf := nativeFrame{
		cmd:   nCmdMessage,
		body:  m.Encoded,
		seqno: pc.getSeqno(),
	}
	pc.transact(&nf, func(f *nativeFrame) {
		switch f.cmd {
		case nCmdRStatus:
			if len(f.body) < 2 {
				actionCB(bwe.M(bwe.PeerError, "short response frame"))
				return
			}
			code := int(binary.LittleEndian.Uint16(f.body))
			if code != bwe.Okay {
				actionCB(bwe.M(code, string(f.body[2:])))
			} else {
				actionCB(nil)
			}
			return
		case nCmdResult:
			nm, err := core.LoadMessage(f.body)
			if err != nil {
				log.Info("dropping incoming query result (malformed message)")
				return
			}
			err = nm.Verify(pc.bwcl.BW())
			if err != nil {
				log.Warnf("dropping incoming query result on uri=%s (failed local validation (%s))", m.Topic, err.Error())
				return
			}
			resultCB(nm)
		case nCmdEnd:
			resultCB(nil)
			//This will be signalled when we unsubscribe
			pc.removeCB(nf.seqno)
		}
	})
}
