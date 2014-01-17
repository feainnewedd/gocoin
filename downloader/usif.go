package main

import (
	"os"
	"fmt"
	"time"
	"sort"
	"bufio"
	"strings"
	"strconv"
	"runtime"
	"runtime/debug"
	"sync/atomic"
)


func readline() string {
	li, _, _ := bufio.NewReader(os.Stdin).ReadLine()
	return string(li)
}


func show_connections() {
	open_connection_mutex.Lock()
	ss := make([]string, len(open_connection_list))
	i := 0
	for _, v := range open_connection_list {
		ss[i] = fmt.Sprintf("%6d  %15s", v.id, v.peerip)
		if !v.isconnected() {
			ss[i] += fmt.Sprint(" - Connecting...")
		} else {
			v.Lock()
			ss[i] += fmt.Sprintf(" %6.1fmin", time.Now().Sub(v.connected_at).Minutes())
			if GetRunPings() {
				ss[i] += fmt.Sprintf(" %6dms", v.avg_ping())
			} else {
				ss[i] += fmt.Sprintf(" %6.2fKB/s", v.bps()/1e3)
			}
			if !v.last_blk_rcvd.IsZero() {
				ss[i] += fmt.Sprintf(" %6.1fsec, %4d bl_in_pr",
					time.Now().Sub(v.last_blk_rcvd).Seconds(), v.inprogress)
			}
			if len(v.send.buf) > 0 {
				ss[i] += fmt.Sprintf("  sending %d", len(v.send.buf))
			}
			v.Unlock()
		}
		i++
	}
	open_connection_mutex.Unlock()
	sort.Strings(ss)
	for i = range ss {
		fmt.Printf("%5d) %s\n", i+1, ss[i])
	}
}


func save_peers() {
	f, _ := os.Create("ips.txt")
	fmt.Fprintf(f, "%d.%d.%d.%d\n", FirstIp[0], FirstIp[1], FirstIp[2], FirstIp[3])
	ccc := 1
	AddrMutex.Lock()
	for k, v := range AddrDatbase {
		if k!=FirstIp && v {
			fmt.Fprintf(f, "%d.%d.%d.%d\n", k[0], k[1], k[2], k[3])
			ccc++
		}
	}
	AddrMutex.Unlock()
	f.Close()
	fmt.Println(ccc, "peers saved")
}

func show_free_mem() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Println("HEAP size", ms.Alloc>>20, "MB,  SysMEM used", ms.Sys>>20, "MB")
}


func usif_prompt() {
	print("cmd> ")
}

func do_usif() {
	time.Sleep(1e9)
	usif_prompt()
	for {
		cmd := readline()
		go func(cmd string) {
			ll := strings.Split(cmd, " ")
			if len(ll)>0 {
				switch ll[0] {
					case "g":
						if GetRunPings() {
							SetRunPings(false)
							fmt.Println("Goto download phase...")
							time.Sleep(3e8)
						} else {
							fmt.Println("Already in download phase?")
						}

					case "a":
						AddrMutex.Lock()
						fmt.Println(len(AddrDatbase), "addressess in the database")
						AddrMutex.Unlock()

					case "q":
						GlobalExit = true
						SetRunPings(false)
						SetDoBlocks(false)
						SetAllHeadersDone(true)
						return

					case "bm":
						fmt.Println("Trying BlocksMutex...")
						BlocksMutex.Lock()
						fmt.Println("BlocksMutex locked.")
						BlocksMutex.Unlock()
						fmt.Println("BlocksMutex unlocked.")

					case "b":
						if TheBlockChain!=nil {
							fmt.Println(TheBlockChain.Stats())
						}

					case "n":
						show_connections()

					case "i":
						print_stats()

					case "c":
						print_counters()

					case "s":
						save_peers()

					case "pr":
						show_inprogress()

					case "pe":
						show_pending()

					case "d":
						if len(ll)>1 {
							n, e := strconv.ParseUint(ll[1], 10, 32)
							if e==nil {
								open_connection_mutex.Lock()
								for _, v := range open_connection_list {
									if v.id==uint32(n) {
										fmt.Println("dropping peer id", n, "...")
										v.setbroken(true)
									}
								}
								open_connection_mutex.Unlock()
							}
						} else {
							if GetRunPings() {
								fmt.Println("dropping longest ping")
								drop_longest_ping()
							} else {
								fmt.Println("dropping slowest peers")
								drop_slowest_peers()
							}
						}

					case "f":
						show_free_mem()
						debug.FreeOSMemory()
						show_free_mem()

					case "m":
						show_free_mem()

					case "mc":
						if len(ll)>1 {
							n, e := strconv.ParseUint(ll[1], 10, 32)
							if e == nil {
								atomic.StoreUint32(&MAX_CONNECTIONS, uint32(n))
								fmt.Println("MAX_CONNECTIONS set to", n)
							}
						}

					default:
						fmt.Println("unknown command:", ll[0])
				}
			}
			usif_prompt()
		}(cmd)
	}
	fmt.Println("do_usif terminated")
}