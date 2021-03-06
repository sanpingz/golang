package main

import (
	"flag"
	"fmt"
	. "github.com/aerospike/aerospike-client-go"
	"hash/fnv"
	"math/rand"
	"runtime"
	"strconv"
	"sync"
	"time"
)

var wg sync.WaitGroup
var errorcount int = 0
var successcount int = 0
var lock sync.Mutex = sync.Mutex{}

func panicOnError(err error) {
	if err != nil {
		panic(err)
	}
}

func countOnError(count *int, err error) {
	if err != nil {
		(*count)++
	}
}

func binMapToBins(bins BinMap) []*Bin {
	binList := make([]*Bin, 0, len(bins))
	for k, v := range bins {
		binList = append(binList, NewBin(k, v))
	}
	return binList
}

func hash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// generates a random strings of specified length
func randString(size int) string {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	const random_alpha_num = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const l = 62
	buf := make([]byte, size)
	for i := 0; i < size; i++ {
		buf[i] = random_alpha_num[rnd.Intn(l)]
	}
	return string(buf)
}

func SlicePut(clnt *Client, policy *WritePolicy, recs []Record) {
	for _, record := range recs {
		if err := clnt.Put(policy, record.Key, record.Bins); err != nil {
			lock.Lock()
			errorcount++
			lock.Unlock()
		} else {
			lock.Lock()
			successcount++
			lock.Unlock()
		}
	}
	defer wg.Done()
}

func RangePut(clnt *Client, policy *WritePolicy, namespace string, setName string, cawlEgId int, start int, stop int) {
	if start > stop {
		panic(fmt.Errorf("Invalid range: [%d, %d)", start, stop))
	}
	// Define bins
	var (
		_skey     *Bin //8 bytes
		_cawlEgId *Bin //8 bytes
		//_cawlEgStatus
		_partitionId   *Bin //8 bytes
		_dataVersion   *Bin //32 bytes
		_cawlEgVersion *Bin //32 bytes
		_state         *Bin //32 bytes
		_scscfPort     *Bin //32 bytes
		_nextTimeout   *Bin //32 bytes
	)
	delta := start
	_cawlEgId = NewBin("cawlEgId", cawlEgId)
	_dataVersion = NewBin("dataVersion", randString(32))
	_cawlEgVersion = NewBin("cawlEgVersion", randString(32))
	_state = NewBin("state", randString(32))
	_scscfPort = NewBin("scscfPort", randString(32))
	_nextTimeout = NewBin("nextTimeout", randString(32))
	for {
		skey := hash(strconv.Itoa(delta))
		key, err := NewKey(namespace, setName, skey)
		_skey = NewBin("skey", skey)
		if err != nil {
			lock.Lock()
			errorcount++
			lock.Unlock()
		} else {
			_partitionId = NewBin("partitionId", NewPartitionByKey(key).PartitionId)
			err := clnt.PutBins(policy, key, _skey, _cawlEgId, _partitionId, _dataVersion, _cawlEgVersion, _state, _scscfPort, _nextTimeout)
			if err != nil {
				lock.Lock()
				errorcount++
				lock.Unlock()
			} else {
				lock.Lock()
				successcount++
				lock.Unlock()
			}
		}
		delta++
		if delta >= stop {
			break
		}
	}
	defer wg.Done()
}

func RangeQuery(client *Client, policy *QueryPolicy, stm *Statement, recsptr *[]Record, errsptr *[]error) {
	recordset, err := client.Query(policy, stm)
	panicOnError(err)
	func() {
	L:
		for {
			select {
			case record, open := <-recordset.Records:
				if !open {
					// scan completed successfully
					break L
				}
				lock.Lock()
				*recsptr = append(*recsptr, *record)
				lock.Unlock()
				/*
					if withWrite {
						err := client.Put(writePolicy, record.Key, record.Bins)
						if err == nil {
							lock.Lock()
							recs = append(recs, record)
							lock.Unlock()
						}else {
							lock.Lock()
							errs = append(errs, err)
							lock.Unlock()
						}
					} else {
						lock.Lock()
						recs = append(recs, record)
						lock.Unlock()
					}
				*/
			case err := <-recordset.Errors:
				if err != nil {
					lock.Lock()
					*errsptr = append(*errsptr, err)
					lock.Unlock()
				}
			default:
				//panic("What happened?")
			}
		}
	}()
	defer wg.Done()
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	hostptr := flag.String("host", "node01", "Host for contact cluster")
	portptr := flag.Int("port", 3000, "Port for contact cluster")
	actionptr := flag.String("action", "load", "Action, load/run")
	recordcountptr := flag.Int("recordcount", 5000000, "Record count")
	failcountptr := flag.Int("failcount", 1, "Failed CawlEg count")
	connQueueSizeptr := flag.Int("connqueue", 1000, "Cached connection queue size")
	threadsptr := flag.Int("threads", 50, "Goroutine count for simulating CawlEg")
	// _PARTITIONS: 4096
	partitionsptr := flag.Int("partitions", 4096, "Partitions number for distributing keys")
	repeatptr := flag.Int("repeat", 1, "Repeat times for scanning")
	namespaceptr := flag.String("ns", "state", "Namespace")
	setNameptr := flag.String("set", "demo", "Set name")
	withWriteptr := flag.Bool("write", false, "With write or not")

	flag.Parse()
	/*
		var Usage = func() {
			fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
			flag.PrintDefaults()
		}
	*/

	host, port, action, recordcount, failcount, connQueueSize,
		threads, partitions, repeat, namespace, setName, withWrite :=
		*hostptr, *portptr, *actionptr, *recordcountptr, *failcountptr, *connQueueSizeptr,
		*threadsptr, *partitionsptr, *repeatptr, *namespaceptr, *setNameptr, *withWriteptr
	_repeat := repeat

	// Default threads
	//_threads := 50
	// Record count per CawlEg
	CPC := recordcount / threads
	// Partitions range per CawlEg for scanning
	RPC := partitions / threads

	clientPolicy := NewClientPolicy()
	writePolicy := NewWritePolicy(0, 0)
	queryPolicy := NewQueryPolicy()
	// Cache lots  connections
	clientPolicy.ConnectionQueueSize = connQueueSize
	// Control the concurrency
	queryPolicy.MaxConcurrentNodes = 0

	client, err := NewClientWithPolicy(clientPolicy, host, port)
	defer client.Close()
	panicOnError(err)

	starttime := time.Now().UnixNano()
REPEAT:
	//recs := []interface{}{}
	//errs := []interface{}{}
	recs := []Record{}
	errs := []error{}
	switch action {
	case "load":
		for cid := 1; cid <= threads; cid++ {
			start := CPC * (cid - 1)
			stop := start + CPC
			go RangePut(client, writePolicy, namespace, setName, cid, start, stop)
			wg.Add(1)
		}
	case "run":
		for cid := 1; cid <= threads; cid++ {
			stm := NewStatement(namespace, setName)
			switch failcount {
			case 1:
				stm.Addfilter(NewEqualFilter("cawlEgId", int64(failcount)))
			default:
				stm.Addfilter(NewRangeFilter("cawlEgId", 1, int64(failcount)))
			}
			_ = RPC
			/*
				var start int = RPC*(cid-1)
				var stop int
				switch cid {
					case threads:
						stop = partitions-1
					default:
						stop = start+RPC-1
				}
				stm.Addfilter(NewRangeFilter("partitionId", int64(start), int64(stop)))
			*/
			go RangeQuery(client, queryPolicy, stm, &recs, &errs)
			wg.Add(1)
			// Only one thead for query
			break
		}

	default:
		panic("Invalid action, action = " + action)
	}
	if action == "run" && _repeat > 1 && repeat > 0 {
		fmt.Printf("Repeat: %d\n", _repeat-repeat+1)
	}
	fmt.Printf("Waiting for all threads\n")
	wg.Wait()

	// Put data back
	writeThread := threads
	if withWrite {
		cnt := len(recs)
		uslice := cnt / writeThread
		var bound0 int = 0
		var bound1 int = uslice - 1
		for {
			if bound1+uslice > cnt-1 {
				bound1 = cnt - 1
			}
			go SlicePut(client, writePolicy, recs[bound0:bound1])
			wg.Add(1)
			bound0 += uslice
			bound1 = bound0 + uslice - 1
			if bound1 > cnt-1 {
				break
			}
		}
		wg.Wait()
	}
	fmt.Printf("All threads done\n")

	//successcount += len(recs)
	//errorcount += len(errs)
	/*
		if errorcount > 0 {
			fmt.Println(errs[0])
		}
	*/
	repeat--
	if action == "run" && repeat > 0 {
		goto REPEAT
	}
	stoptime := time.Now().UnixNano()
	interval := float32(stoptime-starttime) / 1000000000
	tps := float32(successcount) / interval
	successcount /= _repeat
	errorcount /= _repeat
	fmt.Println()
	fmt.Printf("Run time: %0.2f sec\n", interval)
	fmt.Printf("Success count: %d\n", successcount)
	fmt.Printf("Error count: %d\n", errorcount)
	fmt.Printf("TPS: %0.2f ops/sec\n", tps)
	if action == "run" {
		fmt.Printf("Scanning time: %0.2f sec\n", interval/float32(_repeat))
	}
}
