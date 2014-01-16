package main

import (
	"sync"
	"time"
	"bytes"
	"sync/atomic"
//	"encoding/hex"
	"github.com/piotrnar/gocoin/btc"
)

const (
	MAX_BLOCKS_AHEAD = 10e3
	MAX_BLOCKS_IM_MEM = 512<<20 // Use up to 512MB of memory for block cache
	BLOCK_TIMEOUT = 2*time.Second

	GETBLOCKS_BYTES_ONCE = 250e3
)


type one_bip struct {
	Height uint32
	Count uint32
	Conns map[uint32]bool
}

var (
	_DoBlocks bool
	BlocksToGet map[uint32][32]byte
	BlocksInProgress map[[32]byte] *one_bip
	BlocksCached map[uint32] *btc.Block
	BlocksMutex sync.Mutex
	BlocksIndex uint32
	BlocksComplete uint32

	DlStartTime time.Time
	DlBytesProcesses, DlBytesDownloaded uint64
)


func GetDoBlocks() (res bool) {
	BlocksMutex.Lock()
	res = _DoBlocks
	BlocksMutex.Unlock()
	return
}

func SetDoBlocks(res bool) {
	BlocksMutex.Lock()
	_DoBlocks = res
	BlocksMutex.Unlock()
}


func show_inprogress() {
	BlocksMutex.Lock()
	defer BlocksMutex.Unlock()
	println("bocks in progress:")
	cnt := 0
	for _, v := range BlocksInProgress {
		cnt++
		println(cnt, v.Height, v.Count)
	}
}


func (c *one_net_conn) getnextblock() {
	var cnt, lensofar int
	b := new(bytes.Buffer)
	vl := new(bytes.Buffer)

	BlocksMutex.Lock()

	blocks_from := BlocksIndex

	avg_len := avg_block_size()
	max_block_forward := uint32(MAX_BLOCKS_IM_MEM / avg_len)
	if max_block_forward > MAX_BLOCKS_AHEAD {
		max_block_forward = MAX_BLOCKS_AHEAD
	}

	for secondloop:=false; lensofar<GETBLOCKS_BYTES_ONCE; secondloop=true {
		if secondloop && BlocksIndex==blocks_from {
			if BlocksComplete == LastBlockHeight {
				SetDoBlocks(false)
				println("all blocks done")
			} else {
				//println("BlocksIndex", BlocksIndex, blocks_from, BlocksComplete)
				COUNTER("WRAP")
				time.Sleep(1e8)
			}
			break
		}


		BlocksIndex++
		if BlocksIndex > BlocksComplete+max_block_forward || BlocksIndex > LastBlockHeight {
			BlocksIndex = BlocksComplete
		}

		if _, done := BlocksCached[BlocksIndex]; done {
			//println(" cached ->", BlocksIndex)
			continue
		}

		bh, ok := BlocksToGet[BlocksIndex]
		if !ok {
			continue
		}

		cbip := BlocksInProgress[bh]
		if cbip==nil {
			cbip = &one_bip{Height:BlocksIndex, Count:1}
			cbip.Conns = make(map[uint32]bool, MAX_CONNECTIONS)
		} else {
			if cbip.Conns[c.id] {
				continue
			}
			cbip.Count++
		}
		cbip.Conns[c.id] = true
		c.inprogress++
		BlocksInProgress[bh] = cbip

		b.Write([]byte{2,0,0,0})
		b.Write(bh[:])
		cnt++
		lensofar += avg_len
	}
	BlocksMutex.Unlock()

	btc.WriteVlen(vl, uint32(cnt))

	c.sendmsg("getdata", append(vl.Bytes(), b.Bytes()...))
	c.last_blk_rcvd = time.Now()
}


const BSLEN = 0x1000

var (
	BSMut sync.Mutex
	BSSum int
	BSCnt int
	BSIdx int
	BSLen [BSLEN]int
)


func blocksize_update(le int) {
	BSMut.Lock()
	BSLen[BSIdx] = le
	BSSum += le
	if BSCnt<BSLEN {
		BSCnt++
	}
	BSIdx = (BSIdx+1) % BSLEN
	BSSum -= BSLen[BSIdx]
	BSMut.Unlock()
}


func avg_block_size() (le int) {
	BSMut.Lock()
	if BSCnt>0 {
		le = BSSum/BSCnt
	} else {
		le = 220
	}
	BSMut.Unlock()
	return
}


func (c *one_net_conn) block(d []byte) {
	BlocksMutex.Lock()
	defer BlocksMutex.Unlock()
	h := btc.NewSha2Hash(d[:80])

	bip := BlocksInProgress[h.Hash]
	if bip==nil || !bip.Conns[c.id] {
		COUNTER("UNEX")
		//println(h.String(), "- already received", bip)
		return
	}

	c.last_blk_rcvd = time.Now()

	delete(bip.Conns, c.id)
	c.Lock()
	c.inprogress--
	c.Unlock()
	atomic.AddUint64(&DlBytesDownloaded, uint64(len(d)))
	blocksize_update(len(d))

	bl, er := btc.NewBlock(d)
	if er != nil {
		println(c.peerip, "-", er.Error())
		c.setbroken(true)
		return
	}

	BlocksCached[bip.Height] = bl
	delete(BlocksToGet, bip.Height)
	delete(BlocksInProgress, h.Hash)

	bl.BuildTxList()
	if !bytes.Equal(btc.GetMerkel(bl.Txs), bl.MerkleRoot) {
		println(c.peerip, " - MerkleRoot mismatch at block", bip.Height)
		c.setbroken(true)
		return
	}

	//println("  got block", height)
}


func (c *one_net_conn) blk_idle() {
	c.Lock()
	doit := c.inprogress==0
	c.Unlock()
	if doit {
		c.getnextblock()
	} else {
		if !c.last_blk_rcvd.Add(BLOCK_TIMEOUT).After(time.Now()) {
			COUNTER("TOUT")
			c.setbroken(true)
		}
	}
}


func drop_slowest_peers() {
	if open_connection_count() < MAX_CONNECTIONS {
		return
	}
	open_connection_mutex.Lock()

	var min_bps float64
	var minbps_rec *one_net_conn
	for _, v := range open_connection_list {
		if v.isbroken() {
			// alerady broken
			continue
		}

		if !v.isconnected() {
			// still connecting
			continue
		}

		if time.Now().Sub(v.connected_at) < 3*time.Second {
			// give him 3 seconds
			continue
		}

		v.Lock()

		if v.bytes_received==0 {
			v.Unlock()
			// if zero bytes received after 3 seconds - drop it!
			v.setbroken(true)
			//println(" -", v.peerip, "- idle")
			COUNTER("IDLE")
			continue
		}

		bps := v.bps()
		v.Unlock()

		if minbps_rec==nil || bps<min_bps {
			minbps_rec = v
			min_bps = bps
		}
	}
	if minbps_rec!=nil {
		//fmt.Printf(" - %s - slowest (%.3f KBps, %d KB)\n", minbps_rec.peerip, min_bps/1e3, minbps_rec.bytes_received>>10)
		COUNTER("SLOW")
		minbps_rec.setbroken(true)
	}

	open_connection_mutex.Unlock()
}


func get_blocks() {
	BlockChain = btc.NewChain(GocoinHomeDir, GenesisBlock, false)
	if btc.AbortNow || BlockChain==nil {
		return
	}

	BlocksInProgress = make(map[[32]byte] *one_bip)
	BlocksCached = make(map[uint32] *btc.Block, len(BlocksToGet))

	//println("opening connections")
	DlStartTime = time.Now()

	SetDoBlocks(true)
	lastdrop := time.Now().Unix()
	for GetDoBlocks() {
		ct := time.Now().Unix()

		BlocksMutex.Lock()
		in := time.Now().Unix()
		for {
			bl, pres := BlocksCached[BlocksComplete+1]
			if !pres {
				break
			}
			BlocksComplete++
			if BlocksComplete > BlocksIndex {
				BlocksIndex = BlocksComplete
			}
			delete(BlocksCached, BlocksComplete)
			if false {
				er, _, _ := BlockChain.CheckBlock(bl)
				if er != nil {
					println(er.Error())
				} else {
					BlockChain.AcceptBlock(bl)
				}
			} else {
				//BlockChain.Blocks.BlockAdd(BlocksComplete, bl)
			}
			atomic.AddUint64(&DlBytesProcesses, uint64(len(bl.Raw)))
            cu := time.Now().Unix()
			if cu!=in {
				in = cu // reschedule once a second
				BlocksMutex.Unlock()
				time.Sleep(time.Millisecond)
				BlocksMutex.Lock()
			}
		}
		BlocksMutex.Unlock()

		time.Sleep(1e8)

		if ct - lastdrop > 15 {
			lastdrop = ct  // drop slowest peers once for awhile
			drop_slowest_peers()
		}

		add_new_connections()
	}
	println("all blocks done...")
}