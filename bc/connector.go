package bc

import (
	"fmt"
	"os"
	"os/signal"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/immesys/bw2/objects"
	"github.com/immesys/bw2/util"
	"github.com/immesys/bw2bc/accounts"
	"github.com/immesys/bw2bc/accounts/keystore"
	"github.com/immesys/bw2bc/accounts/usbwallet"
	"github.com/immesys/bw2bc/cmd/utils"
	"github.com/immesys/bw2bc/common"
	"github.com/immesys/bw2bc/common/hexutil"
	"github.com/immesys/bw2bc/core"
	"github.com/immesys/bw2bc/eth"
	"github.com/immesys/bw2bc/eth/filters"
	"github.com/immesys/bw2bc/les"
	"github.com/immesys/bw2bc/log"
	"github.com/immesys/bw2bc/node"
	"github.com/immesys/bw2bc/p2p/discover"
	"github.com/immesys/bw2bc/p2p/nat"
	"github.com/immesys/bw2bc/params"
	"github.com/immesys/bw2bc/rlp"
)

const (
	DefGasPrice          = "10000000000000" // 10 Szabo
	GpoMinGasPrice       = DefGasPrice
	GpoMaxGasPrice       = "1000000000000000" // 1 finney
	DefaultConfirmations = 2
	DefaultTimeout       = 20
)

type blockChain struct {
	ks *entityKeyStore
	//	x     *xeth.XEth
	am    *accounts.Manager
	fm    *filters.FilterSystem
	eth   *eth.Ethereum
	nd    *node.Node
	shdwn chan bool

	api_txpool    *eth.PublicTxPoolAPI
	api_privadmin *node.PrivateAdminAPI
	api_pubadmin  *node.PublicAdminAPI
	api_pubchain  *eth.PublicBlockChainAPI
	api_pubtx     *eth.PublicTransactionPoolAPI
	api_privacct  *eth.PrivateAccountAPI
	api_pubeth    *eth.PublicEthereumAPI
}

type bcClient struct {
	bc                   *blockChain
	ent                  *objects.Entity
	acc                  int
	DefaultConfirmations uint64
	DefaultTimeout       uint64
}

var BOSSWAVEBootNodes = []*discover.Node{
	// BOSSWAVE boot nodes
	//boota ipv4
	discover.MustParseNode("enode://6ae73d0621c9c9a6bdac4a332900f1f57ea927f1a03aef5c2ffffa70fca0fada636da3ceac45ee4a2addbdb2bdbe9cb129b3a098d57fa09ff451712ac9c80fc9@54.215.189.111:30301"),
	//boota ipv6
	discover.MustParseNode("enode://6ae73d0621c9c9a6bdac4a332900f1f57ea927f1a03aef5c2ffffa70fca0fada636da3ceac45ee4a2addbdb2bdbe9cb129b3a098d57fa09ff451712ac9c80fc9@[2600:1f1c:c2f:a400:2f8f:3b34:1f55:3f7a]:30301"),
	//bootb ipv4
	discover.MustParseNode("enode://832c5a520a1079190e9fb57827306ee3882231077a3c543c8cae4c3a386703b3a4e0fd3ca9cb6b00b0d5482efc3e4dd8aafdb7fedb061d74a9d500f230e45873@54.183.54.213:30301"),
	//bootb ipv6
	discover.MustParseNode("enode://832c5a520a1079190e9fb57827306ee3882231077a3c543c8cae4c3a386703b3a4e0fd3ca9cb6b00b0d5482efc3e4dd8aafdb7fedb061d74a9d500f230e45873@[2600:1f1c:c2f:a400:5c38:c2f5:7e26:841c]:30301"),
	// Asylum
	discover.MustParseNode("enode://686f709677c4d0f2cd58cf651ea8ce1375bef22dcf29065994e34c1c4fd6f86691698321460f43059cc6cea536cd66ef534208869cd27765c4455f577a42a107@128.32.37.241:30303"),
}

func N() (BlockChainProvider, chan bool) {

	var optIdentity string
	var optLightClient bool
	var optEnableJIT bool
	var optKeystoreDir string
	var optDatadir string
	var optMaxPeers int
	var optMaxPendingPeers int

	optIdentity = "identd"
	optLightClient = false
	optEnableJIT = false
	optKeystoreDir = "/home/michael/.bw2bcdev/ks/"
	optDatadir = "/home/michael/.bw2bcdev/dd/"
	optMaxPeers = 20
	optMaxPendingPeers = 3

	// Create the default extradata and construct the base node
	var clientInfo = struct {
		Version   uint
		Name      string
		GoVersion string
		Os        string
	}{uint(params.VersionMajor<<16 | params.VersionMinor<<8 | params.VersionPatch), util.BW2Version, runtime.Version(), runtime.GOOS}
	extra, err := rlp.EncodeToBytes(clientInfo)
	if err != nil {
		log.Warn("Failed to set canonical miner information", "err", err)
	}
	if uint64(len(extra)) > params.MaximumExtraDataSize {
		log.Warn("Miner extra data exceed limit", "extra", hexutil.Bytes(extra), "limit", params.MaximumExtraDataSize)
		extra = nil
	}

	//This is from utils.MakeNode
	vsn := util.BW2Version

	// if we're running a light client or server, force enable the v5 peer discovery unless it is explicitly disabled with --nodiscover
	// note that explicitly specifying --v5disc overrides --nodiscover, in which case the later only disables v4 discovery
	forceV5Discovery := (ctx.GlobalBool(LightModeFlag.Name) || ctx.GlobalInt(LightServFlag.Name) > 0) && !ctx.GlobalBool(NoDiscoverFlag.Name)

	var comps []string
	if optIdentity != "" {
		comps = append(comps, optIdentity)
	}
	if optEnableJIT {
		comps = append(comps, "JIT")
	}
	nati, err := nat.Parse("any")
	if err != nil {
		panic(err)
	}
	nodeUserIdent := strings.Join(comps, "/")
	config := &node.Config{
		DataDir:           optDatadir,
		KeyStoreDir:       optKeystoreDir,
		UseLightweightKDF: true,
		PrivateKey:        nil,
		Name:              "bw2bc",
		Version:           vsn,
		UserIdent:         nodeUserIdent,
		NoDiscovery:       true, //Only use v5
		DiscoveryV5:       true,
		DiscoveryV5Addr:   ":30304",
		BootstrapNodes:    nil,
		BootstrapNodesV5:  BOSSWAVEBootNodes,
		ListenAddr:        ":30302",
		NAT:               nati,
		MaxPeers:          optMaxPeers,
		MaxPendingPeers:   optMaxPendingPeers,
		IPCPath:           "",
		HTTPHost:          "",
		HTTPPort:          0,
		HTTPCors:          "",
		HTTPModules:       []string{},
		WSHost:            "",
		WSPort:            0,
		WSOrigins:         []string{},
		WSModules:         []string{},
	}

	stack, err := node.New(config)
	if err != nil {
		Fatalf("Failed to create the protocol stack: %v", err)
	}

	backends := []accounts.Backend{
		...
	}
	am := accounts.NewManager(backends...)
	stack.SetAccMan(m)

	cconfig := new(params.ChainConfig)

	// Homestead fork
	cconfig.HomesteadBlock = params.MainNetHomesteadBlock
	// DAO fork
	cconfig.DAOForkBlock = params.MainNetDAOForkBlock
	cconfig.DAOForkSupport = true

	// DoS reprice fork
	cconfig.EIP150Block = params.MainNetHomesteadGasRepriceBlock
	cconfig.EIP150Hash = params.MainNetHomesteadGasRepriceHash

	// DoS state cleanup fork
	cconfig.EIP155Block = params.MainNetSpuriousDragon
	cconfig.EIP158Block = params.MainNetSpuriousDragon
	cconfig.ChainId = params.MainNetChainID

	ethConf := &eth.Config{
		Etherbase:               common.Address{},
		ChainConfig:             cconfig,
		FastSync:                true,
		LightMode:               optLightClient,
		LightServ:               optSupportLight,
		LightPeers:              10,
		MaxPeers:                optMaxPeers,
		DatabaseCache:           128,
		DatabaseHandles:         utils.MakeDatabaseHandles(),
		NetworkId:               28589,
		MinerThreads:            0,
		ExtraData:               nil,
		DocRoot:                 "",
		GasPrice:                common.String2Big(DefGasPrice),
		GpoMinGasPrice:          common.String2Big(GpoMinGasPrice),
		GpoMaxGasPrice:          common.String2Big(GpoMaxGasPrice),
		GpoFullBlockRatio:       80,
		GpobaseStepDown:         10,
		GpobaseStepUp:           100,
		GpobaseCorrectionFactor: 110,
		SolcPath:                "",
		EthashCacheDir:          optEthashCacheDir,
		EthashCachesInMem:       1,
		EthashCachesOnDisk:      2,
		EthashDatasetDir:        optEthashDatasetDir,
		EthashDatasetsInMem:     1,
		EthashDatasetsOnDisk:    2,
		EnablePreimageRecording: false,
	}

	if ethConf.LightMode {
		if err := stack.Register(func(ctx *node.ServiceContext) (node.Service, error) {
			return les.New(ctx, ethConf)
		}); err != nil {
			Fatalf("Failed to register the Ethereum light node service: %v", err)
		}
	} else {
		if err := stack.Register(func(ctx *node.ServiceContext) (node.Service, error) {
			fullNode, err := eth.New(ctx, ethConf)
			if fullNode != nil && ethConf.LightServ > 0 {
				ls, _ := les.NewLesServer(fullNode, ethConf)
				fullNode.AddLesServer(ls)
			}
			return fullNode, err
		}); err != nil {
			Fatalf("Failed to register the Ethereum full node service: %v", err)
		}
	}

	//XTAG do this
	// Add the Ethereum Stats daemon if requested
	if url := ctx.GlobalString(utils.EthStatsURLFlag.Name); url != "" {
		utils.RegisterEthStatsService(stack, url)
	}
	//
	// // Add the release oracle service so it boots along with node.
	// if err := stack.Register(func(ctx *node.ServiceContext) (node.Service, error) {
	// 	config := release.Config{
	// 		Oracle: relOracle,
	// 		Major:  uint32(params.VersionMajor),
	// 		Minor:  uint32(params.VersionMinor),
	// 		Patch:  uint32(params.VersionPatch),
	// 	}
	// 	commit, _ := hex.DecodeString(gitCommit)
	// 	copy(config.Commit[:], commit)
	// 	return release.NewReleaseService(ctx, config)
	// }); err != nil {
	// 	utils.Fatalf("Failed to register the Geth release oracle service: %v", err)
	// }

	// Start up the node itself
	utils.StartNode(stack)

	// // Unlock any account specifically requested
	// ks := stack.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)
	//
	// passwords := utils.MakePasswordList(ctx)
	// unlocks := strings.Split(ctx.GlobalString(utils.UnlockedAccountFlag.Name), ",")
	// for i, account := range unlocks {
	// 	if trimmed := strings.TrimSpace(account); trimmed != "" {
	// 		unlockAccount(ctx, ks, trimmed, i, passwords)
	// 	}
	// }
	// // Register wallet event handlers to open and auto-derive wallets
	// events := make(chan accounts.WalletEvent, 16)
	// stack.AccountManager().Subscribe(events)
	//
	// go func() {
	// 	// Create an chain state reader for self-derivation
	// 	rpcClient, err := stack.Attach()
	// 	if err != nil {
	// 		utils.Fatalf("Failed to attach to self: %v", err)
	// 	}
	// 	stateReader := ethclient.NewClient(rpcClient)
	//
	// 	// Open and self derive any wallets already attached
	// 	for _, wallet := range stack.AccountManager().Wallets() {
	// 		if err := wallet.Open(""); err != nil {
	// 			log.Warn("Failed to open wallet", "url", wallet.URL(), "err", err)
	// 		} else {
	// 			wallet.SelfDerive(accounts.DefaultBaseDerivationPath, stateReader)
	// 		}
	// 	}
	// 	// Listen for wallet event till termination
	// 	for event := range events {
	// 		if event.Arrive {
	// 			if err := event.Wallet.Open(""); err != nil {
	// 				log.Warn("New wallet appeared, failed to open", "url", event.Wallet.URL(), "err", err)
	// 			} else {
	// 				log.Info("New wallet appeared", "url", event.Wallet.URL(), "status", event.Wallet.Status())
	// 				event.Wallet.SelfDerive(accounts.DefaultBaseDerivationPath, stateReader)
	// 			}
	// 		} else {
	// 			log.Info("Old wallet dropped", "url", event.Wallet.URL())
	// 			event.Wallet.Close()
	// 		}
	// 	}
	// }()
	// Start auxiliary services if enabled
	//register the APIs
	var ethi *eth.Ethereum
	err = rv.nd.Service(&ethi)
	if err != nil {
		panic(err)
	}
	rv.eth = ethi
	rv.api_txpool = eth.NewPublicTxPoolAPI(ethi)
	rv.api_privadmin = node.NewPrivateAdminAPI(rv.nd)
	rv.api_pubadmin = node.NewPublicAdminAPI(rv.nd)
	rv.api_pubchain = eth.NewPublicBlockChainAPI_S(ethi)
	rv.api_pubtx = eth.NewPublicTransactionPoolAPI(ethi)
	rv.api_privacct = eth.NewPrivateAccountAPI(ethi)
	rv.api_pubeth = eth.NewPublicEthereumAPI(ethi)
	rv.fm = filters.NewFilterSystem(rv.eth.EventMux())

	//	eth.NewPublicBlockChainAPI(config *core.ChainConfig, bc *core.BlockChain, m *miner.Miner, chainDb ethdb.Database, gpo *eth.GasPriceOracle, eventMux *event.TypeMux, am *accounts.Manager)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		rv.nd.Stop()
		rv.shdwn <- true
	}()
	go rv.DebugTXPoolLoop()
	return rv, rv.shdwn
}
func NewBlockChain(datadir string) (BlockChainProvider, chan bool) {
	keydir := path.Join(datadir, "keys")

	// glog.SetV(3)
	// glog.CopyStandardLogTo("INFO")
	// glog.SetToStderr(true)
	// glog.SetLogDir(datadir)

	os.MkdirAll(datadir, os.ModeDir|0777)
	os.MkdirAll(keydir, os.ModeDir|0777)
	rv := &blockChain{
		ks:    NewEntityKeyStore(),
		shdwn: make(chan bool, 1),
	}
	rv.am = accounts.NewManagerI(rv.ks, keydir)

	fmt.Printf("USING MAX PEER LIMIT: %d\n", DefaultMaxPeers)
	// Configure the node's service container
	stackConf := &node.Config{
		DataDir:         datadir,
		PrivateKey:      nil,
		Name:            common.MakeName("BW2", util.BW2Version),
		NoDiscovery:     false,
		BootstrapNodes:  BOSSWAVEBootNodes,
		ListenAddr:      ":30302",
		NAT:             nil,
		MaxPeers:        DefaultMaxPeers,
		MaxPendingPeers: 0,
		IPCPath:         "",
		HTTPHost:        "",
		HTTPPort:        80,
		HTTPCors:        "",
		HTTPModules:     []string{},
		WSHost:          "",
		WSPort:          81,
		WSOrigins:       "",
		WSModules:       []string{},
	}
	// Configure the Ethereum service

	ethConf := &eth.Config{
		ChainConfig:             &core.ChainConfig{HomesteadBlock: params.MainNetHomesteadBlock},
		Genesis:                 "",
		FastSync:                true,
		BlockChainVersion:       3,
		DatabaseCache:           DefaultDBCache,
		DatabaseHandles:         utils.MakeDatabaseHandles(),
		NetworkId:               28589,
		AccountManager:          rv.am,
		Etherbase:               common.Address{},
		MinerThreads:            0,
		ExtraData:               []byte{},
		NatSpec:                 false,
		DocRoot:                 "",
		EnableJit:               false,
		ForceJit:                false,
		GasPrice:                common.String2Big(DefGasPrice),
		GpoMinGasPrice:          common.String2Big(GpoMinGasPrice),
		GpoMaxGasPrice:          common.String2Big(GpoMaxGasPrice),
		GpoFullBlockRatio:       80,
		GpobaseStepDown:         10,
		GpobaseStepUp:           100,
		GpobaseCorrectionFactor: 110,
		SolcPath:                "",
		AutoDAG:                 false,
	}

	// Assemble and return the protocol stack
	stack, err := node.New(stackConf)
	if err != nil {
		panic("Failed to create the protocol stack: " + err.Error())
	}
	if err := stack.Register(func(ctx *node.ServiceContext) (node.Service, error) {
		return eth.New(ctx, ethConf)
	}); err != nil {
		panic("Failed to register the Ethereum service: " + err.Error())
	}

	rv.nd = stack
	// Start up the node itself
	utils.StartNode(rv.nd)

	//register the APIs
	var ethi *eth.Ethereum
	err = rv.nd.Service(&ethi)
	if err != nil {
		panic(err)
	}
	rv.eth = ethi
	rv.api_txpool = eth.NewPublicTxPoolAPI(ethi)
	rv.api_privadmin = node.NewPrivateAdminAPI(rv.nd)
	rv.api_pubadmin = node.NewPublicAdminAPI(rv.nd)
	rv.api_pubchain = eth.NewPublicBlockChainAPI_S(ethi)
	rv.api_pubtx = eth.NewPublicTransactionPoolAPI(ethi)
	rv.api_privacct = eth.NewPrivateAccountAPI(ethi)
	rv.api_pubeth = eth.NewPublicEthereumAPI(ethi)
	rv.fm = filters.NewFilterSystem(rv.eth.EventMux())

	//	eth.NewPublicBlockChainAPI(config *core.ChainConfig, bc *core.BlockChain, m *miner.Miner, chainDb ethdb.Database, gpo *eth.GasPriceOracle, eventMux *event.TypeMux, am *accounts.Manager)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		rv.nd.Stop()
		rv.shdwn <- true
	}()
	go rv.DebugTXPoolLoop()
	return rv, rv.shdwn
}

/*
func NewBlockChain(datadir string) (BlockChainProvider, chan bool) {

	os.MkdirAll(datadir, os.ModeDir|0777)
	glog.SetV(2)
	glog.CopyStandardLogTo("INFO")
	glog.SetToStderr(true)
	glog.SetLogDir(datadir)

	rv := &blockChain{
		ks:    NewEntityKeyStore(),
		shdwn: make(chan bool, 1),
	}
	natThing, _ := nat.Parse("")
	front := &frontend{bc: rv}
	rv.am = accounts.NewManager(rv.ks)
	// Assemble the entire eth configuration
	cfg := &eth.Config{
		Name:                    common.MakeName("BW2", util.BW2Version),
		DataDir:                 datadir,
		GenesisFile:             "",
		FastSync:                false,
		BlockChainVersion:       core.BlockChainVersion,
		DatabaseCache:           0,
		SkipBcVersionCheck:      false,
		NetworkId:               eth.NetworkId,
		LogFile:                 "logfile",
		Verbosity:               2,
		Etherbase:               common.Address{},
		MinerThreads:            0,
		AccountManager:          rv.am,
		VmDebug:                 false,
		MaxPeers:                25,
		MaxPendingPeers:         0,
		Port:                    "30303",
		Olympic:                 false,
		NAT:                     natThing,
		NatSpec:                 false,
		DocRoot:                 filepath.Join(datadir, "docroot"),
		Discovery:               true,
		NodeKey:                 nil,
		Shh:                     false,
		Dial:                    true,
		BootNodes:               "",
		GasPrice:                common.String2Big(DefGasPrice),
		GpoMinGasPrice:          common.String2Big(GpoMinGasPrice),
		GpoMaxGasPrice:          common.String2Big(GpoMaxGasPrice),
		GpoFullBlockRatio:       80,
		GpobaseStepDown:         10,
		GpobaseStepUp:           100,
		GpobaseCorrectionFactor: 110,
		SolcPath:                "",
		AutoDAG:                 false,
	}
	var err error
	rv.eth, err = eth.New(cfg)
	if err != nil {
		utils.Fatalf("%v", err)
	}
	utils.StartEthereum(rv.eth)
	rv.fm = filters.NewFilterSystem(rv.eth.EventMux())
	rv.x = xeth.New(rv.eth, front)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		rv.x.Stop()
		rv.shdwn <- true
	}()
	go rv.DebugTXPoolLoop()
	return rv, rv.shdwn
}
*/

func (bc *blockChain) DebugTXPoolLoop() {
	for {
		time.Sleep(2 * time.Second)
		p := bc.api_txpool.Inspect()
		for k, v := range p["pending"] {
			fmt.Println("P1: ", k, v)
		}
		for k, v := range p["queued"] {
			fmt.Println("P2: ", k, v)
		}
		//fmt.Println("P:", p)
		//	peers, e := bc.api_pubadmin.Peers()
		//	if e != nil {
		//		panic(e)
		//	}
		//	fmt.Printf("peers:\n %#v", peers)
		/*for i, v := range bc.eth.TxPool().GetTransactions() {
			if i == 0 {
				fmt.Println()
			}
			fmt.Println("TX ", i)
			fmt.Println(v.String())
		}*/
	}
}

func (bc *blockChain) ENode() string {
	ni, err := bc.api_pubadmin.NodeInfo()
	if err != nil {
		panic(err)
	}
	return ni.Enode
}

func (bc *blockChain) GetClient(ent *objects.Entity) BlockChainClient {
	rv := &bcClient{
		bc:                   bc,
		ent:                  ent,
		DefaultConfirmations: DefaultConfirmations,
		DefaultTimeout:       DefaultTimeout,
	}
	bc.ks.AddEntity(ent)
	return rv
}

func (bcc *bcClient) SetEntity(ent *objects.Entity) {
	bcc.ent = ent
	bcc.acc = 0
	//This might be a new entity
	bcc.bc.ks.AddEntity(ent)
}

/*
func (bc *blockChain) Shutdown() {
	bc.nd.Stop()
}*/

// Frontend stuff
/*
type frontend struct {
	bc *blockChain
}

func (f *frontend) AskPassword() (string, bool) {
	return "", true
}
func (f *frontend) UnlockAccount(address []byte) bool {
	e := f.bc.am.Unlock(common.BytesToAddress(address), "")
	if e != nil {
		panic(e)
	}
	return true
}
func (f *frontend) ConfirmTransaction(tx string) bool {
	return true
}*/
