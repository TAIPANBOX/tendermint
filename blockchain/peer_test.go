package blockchain

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cmn "github.com/tendermint/tendermint/libs/common"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/p2p"
)

// These vars are used to record and verify different events generated during tests
var (
	numErrFuncCalls int        // number of calls to the errFunc
	lastErr         error      // last generated error
	peerTestMtx     sync.Mutex // needed as modifications of these variables are done from timer handler goroutine also
)

func resetErrors() {
	peerTestMtx.Lock()
	defer peerTestMtx.Unlock()
	numErrFuncCalls = 0
	lastErr = nil
}

func errFunc(err error, peerID p2p.ID) {
	peerTestMtx.Lock()
	defer peerTestMtx.Unlock()
	_ = peerID
	lastErr = err
	numErrFuncCalls++
}

// check if peer timer is running or not (a running timer can be successfully stopped)
// Note: it does stop the timer!
func checkByStoppingPeerTimer(t *testing.T, peer *bpPeer, running bool) {
	assert.NotPanics(t, func() {
		stopped := peer.timeout.Stop()
		if running {
			assert.True(t, stopped)
		} else {
			assert.False(t, stopped)
		}
	})
}

func TestPeerResetMonitor(t *testing.T) {
	peer := newBPPeer(p2p.ID(cmn.RandStr(12)), 10, errFunc)
	peer.setLogger(log.TestingLogger())
	peer.resetMonitor()
	assert.NotNil(t, peer.recvMonitor)
}

func TestPeerTimer(t *testing.T) {
	peerTimeout = 2 * time.Millisecond

	peer := newBPPeer(p2p.ID(cmn.RandStr(12)), 10, errFunc)
	peer.setLogger(log.TestingLogger())
	assert.Nil(t, peer.timeout)

	// initial reset call with peer having a nil timer
	peer.resetTimeout()
	assert.NotNil(t, peer.timeout)
	// make sure timer is running and stop it
	checkByStoppingPeerTimer(t, peer, true)

	// reset with non nil expired timer
	peer.resetTimeout()
	assert.NotNil(t, peer.timeout)
	// make sure timer is running and stop it
	checkByStoppingPeerTimer(t, peer, true)
	resetErrors()

	// reset with running timer (started above)
	time.Sleep(time.Millisecond)
	peer.resetTimeout()
	assert.NotNil(t, peer.timeout)

	// let the timer expire and ...
	time.Sleep(3 * time.Millisecond)
	checkByStoppingPeerTimer(t, peer, false)

	peerTestMtx.Lock()
	// ... check an error has been sent, error is peerNonResponsive
	assert.Equal(t, 1, numErrFuncCalls)
	assert.Equal(t, lastErr, errNoPeerResponse)
	peerTestMtx.Unlock()

	// Restore the peerTimeout to its original value
	peerTimeout = defaultPeerTimeout
}

func TestPeerIncrPending(t *testing.T) {
	peerTimeout = 2 * time.Millisecond

	peer := newBPPeer(p2p.ID(cmn.RandStr(12)), 10, errFunc)
	peer.setLogger(log.TestingLogger())

	peer.incrPending()
	assert.NotNil(t, peer.recvMonitor)
	assert.NotNil(t, peer.timeout)
	assert.Equal(t, int32(1), peer.numPending)

	peer.incrPending()
	assert.NotNil(t, peer.recvMonitor)
	assert.NotNil(t, peer.timeout)
	assert.Equal(t, int32(2), peer.numPending)

	// Restore the peerTimeout to its original value
	peerTimeout = defaultPeerTimeout
}

func TestPeerDecrPending(t *testing.T) {
	peerTimeout = 2 * time.Millisecond

	peer := newBPPeer(p2p.ID(cmn.RandStr(12)), 10, errFunc)
	peer.setLogger(log.TestingLogger())

	// panic if numPending is 0 and try to decrement it
	assert.Panics(t, func() { peer.decrPending(10) })

	// decrement to zero
	peer.incrPending()
	peer.decrPending(10)
	assert.Equal(t, int32(0), peer.numPending)
	// make sure timer is not running
	checkByStoppingPeerTimer(t, peer, false)

	// decrement to non zero
	peer.incrPending()
	peer.incrPending()
	peer.decrPending(10)
	assert.Equal(t, int32(1), peer.numPending)
	// make sure timer is running and stop it
	checkByStoppingPeerTimer(t, peer, true)

	// Restore the peerTimeout to its original value
	peerTimeout = defaultPeerTimeout
}

func TestPeerCanBeRemovedDueToExpiration(t *testing.T) {
	minRecvRate = int64(100) // 100 bytes/sec exponential moving average

	peer := newBPPeer(p2p.ID(cmn.RandStr(12)), 10, errFunc)
	peer.setLogger(log.TestingLogger())

	peerTimeout = time.Millisecond
	peer.incrPending()
	time.Sleep(2 * time.Millisecond)
	// timer expired, should be able to remove peer
	peerTestMtx.Lock()
	assert.Equal(t, errNoPeerResponse, lastErr)
	peerTestMtx.Unlock()

	// Restore the peerTimeout to its original value
	peerTimeout = defaultPeerTimeout

}

func TestPeerCanBeRemovedDueToLowSpeed(t *testing.T) {
	minRecvRate = int64(100) // 100 bytes/sec exponential moving average

	peer := newBPPeer(p2p.ID(cmn.RandStr(12)), 10, errFunc)
	peer.setLogger(log.TestingLogger())

	peerTimeout = time.Second
	peerSampleRate = 0
	peerWindowSize = 0

	peer.incrPending()
	peer.numPending = 100

	// monitor starts with a higher rEMA (~ 2*minRecvRate), wait for it to go down
	time.Sleep(900 * time.Millisecond)

	// normal peer - send a bit more than 100 bytes/sec, > 10 bytes/100msec, check peer is not considered slow
	for i := 0; i < 10; i++ {
		peer.decrPending(11)
		time.Sleep(100 * time.Millisecond)
		require.Nil(t, peer.isGood())
	}

	// slow peer - send a bit less than 10 bytes/100msec
	for i := 0; i < 10; i++ {
		peer.decrPending(9)
		time.Sleep(100 * time.Millisecond)
	}
	// check peer is considered slow
	assert.Equal(t, errSlowPeer, peer.isGood())

}

func TestPeerCleanup(t *testing.T) {

	peer := newBPPeer(p2p.ID(cmn.RandStr(12)), 10, errFunc)
	peer.setLogger(log.TestingLogger())

	peerTimeout = 2 * time.Millisecond
	assert.Nil(t, peer.timeout)

	// initial reset call with peer having a nil timer
	peer.resetTimeout()
	assert.NotNil(t, peer.timeout)

	peerTestMtx.Lock()
	peer.cleanup()
	peerTestMtx.Unlock()

	checkByStoppingPeerTimer(t, peer, false)
	// Restore the peerTimeout to its original value
	peerTimeout = defaultPeerTimeout
}