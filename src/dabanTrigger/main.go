package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"lib"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"trade"
)

var (
	listen       = flag.String("listen", ":31866", "http监听地址")
	hqCenterAddr = flag.String("hqCenterAddr", "http://127.0.0.1:31800", "行情服务器地址")
	tdCenterAddr = flag.String("tdCenterAddr", "http://127.0.0.1:31888", "委托服务器地址")
	localCbAddr  = flag.String("localCbAddr", "http://127.0.0.1:31866/cb", "对orderHolder提供的回调地址")
	rePlayFile   = flag.String("rePlay", "", "向hqCenter指定回放行情文件")
	sharesFile   = flag.String("sharesFile", "./data/shares.json", "流通股映射文件,用来计算换手率,json格式")
	confFile     = flag.String("confFile", "./data/trigger.json", "触发条件配置文件")
)

var (
	err             error
	stockSharesMap  map[string]float64
	codeHqSet       sync.Map
	codeLatestHqMap sync.Map
	codeHqWindow    sync.Map
	selectConf      []triggerConf
)

var (
	hqRbChan        = make(chan []byte, 1024)
	date            = lib.GetDate()
	allHoldCodeMap  = sync.Map{}
	confKeyItemMap  = sync.Map{}
	confOpenInfoMap = sync.Map{}
	confOpenTmpMap  = sync.Map{} //记录conf已发单但尚未买入信息，买成后清除
	confIdSwitchMap = sync.Map{} //conf开关位
	lv1HqWindowLen  = 60
	lv1RawChan      = make(chan string, 1024)
	lv2RawChan      = make(chan string, 1024)
	morningStart    = "09:30:00"
	stop            = make(chan os.Signal, 1)
)

type triggerConf struct {
	HoldCnt  float64 `json:"holdCnt"`  //最多同时持仓
	Amt      float64 `json:"amt"`      //单仓金额
	Vol      float64 `json:"vol"`      //不为0则为定额单仓模式
	BWait    float64 `json:"bWait"`    //买单超时时间
	RaTh     float64 `json:"raTh"`     //触发时最低涨幅阈值
	Sec      float64 `json:"sec"`      //观测窗口时长
	RaRate   float64 `json:"raRate"`   //观测期秒均涨幅
	TnRate   float64 `json:"tnRate"`   //观测期秒均换手
	StockAmt float64 `json:"stockAmt"` //窗口期间每秒成交额下限
}
type keyStatusItem struct {
	Key      string  `json:"key"`
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	BStatus  string  `json:"b_status"`
	BOPrice  float64 `json:"b_oPrice"`
	BOVolume float64 `json:"b_oVolume"`
	BDPrice  float64 `json:"b_dPrice"`
	BDVolume float64 `json:"b_dVolume"`
	BOTime   string  `json:"b_oTime"`
	BOStamp  int64   `json:"b_oStamp"`
	BDStamp  int64   `json:"b_dStamp"`
	SStatus  string  `json:"s_status"`
	SOPrice  float64 `json:"s_oPrice"`
	SOVolume float64 `json:"s_oVolume"`
	SDPrice  float64 `json:"s_dPrice"`
	SDVolume float64 `json:"s_dVolume"`
	SOStamp  int64   `json:"s_oStamp"`
	SDStamp  int64   `json:"s_dStamp"`
	SDTime   string  `json:"s_dTime"`
	Earn     float64 `json:"earn"`
	BOid     string  `json:"boid"`
	FsOid    string  `json:"fsoid"` //首次卖单id,用来判断后续卖单是否为撤单再卖
	SOid     string  `json:"soid"`
}
type lv1HqMap struct {
	time     string
	code     string
	name     string
	price    float64
	ratio    float64
	volume   float64
	amount   float64
	stamp    float64
	turnover float64
	b1       float64
	b1p      float64
	b2       float64
	b2p      float64
	b3       float64
	b3p      float64
	b4       float64
	b4p      float64
	b5       float64
	b5p      float64
	s1       float64
	s1p      float64
	s2       float64
	s2p      float64
	s3       float64
	s3p      float64
	s4       float64
	s4p      float64
	s5       float64
	s5p      float64
}

type confOpenInfo struct {
	cnt     int
	hold    int
	earn    float64
	holdMap sync.Map
}
type httpEngine struct {
}

func (engine *httpEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	lib.SetupCORS(&w)
	rb := []byte{}
	rstMap := map[string]string{}
	path := strings.ToLower(r.URL.Path)
	path = strings.TrimLeft(path, "/")
	pathEx := strings.Split(path, "/")
	ipInfo := strings.Split(r.RemoteAddr, ":")
	clientIp := ipInfo[0]
	_ = clientIp

	r.ParseForm()
	pushDatas := r.Form["data"]
	pushData := r.URL.Query().Get("data") //接收回传信息
	cid := r.URL.Query().Get("cid")       //指定操作confid
	op := r.URL.Query().Get("op")         //接收操作信息
	rstMap["code"] = "0"
	rstMap["message"] = ""
	if r.Method == http.MethodPost && len(pushDatas) > 0 {
		pushData = pushDatas[0]
	}

	keyItem := keyStatusItem{}
	err = json.Unmarshal([]byte(pushData), &keyItem)

	if pathEx[0] == "cb" { //接收交易回调
		if lib.IsStructEmpty(keyItem) {
			rstMap["code"] = "-1"
			rstMap["message"] = "回传keyItem为空"
			rb, _ = json.Marshal(rstMap)
			fmt.Fprintf(w, "%s", rb)
			return
		}
		dealCb(keyItem)
		rb, _ = json.Marshal(rstMap)
		fmt.Fprintf(w, "%s", rb)
		return
	}

	if pathEx[0] == "sum" { //获取汇总
		infoList := [][]float64{}
		earnList := []float64{}
		cntList := []float64{}
		holdList := []float64{}
		switchList := []float64{}
		for confId, conf := range selectConf {
			earn := 0.0
			cnt := 0
			hold := 0
			if vT, ok := confOpenInfoMap.Load(conf); ok {
				info := vT.(*confOpenInfo)
				earn = info.earn
				cnt = info.cnt
				hold = info.hold
			}
			confOn := 1
			if vT, ok := confIdSwitchMap.Load(confId); ok {
				isOn := vT.(bool)
				if !isOn {
					confOn = 0
				}
			}
			earnList = append(earnList, earn)
			cntList = append(cntList, float64(cnt))
			holdList = append(holdList, float64(hold))
			switchList = append(switchList, float64(confOn))
		}
		infoList = append(infoList, switchList)
		infoList = append(infoList, holdList)
		infoList = append(infoList, cntList)
		infoList = append(infoList, earnList)
		rb, _ = json.Marshal(infoList)
		fmt.Fprintf(w, "%s", rb)
		return
	}

	if pathEx[0] == "ctl" { //接收控制
		rstMap["code"] = "0"
		rstMap["message"] = "策略未定义控制:" + op

		if op == "exit" {
			stop <- os.Signal(syscall.SIGTERM)
			rstMap["message"] = "策略服务控制退出:" + op
			rb, _ = json.Marshal(rstMap)
			fmt.Fprintf(w, "%s", rb)
			return
		}

		if cid != "" {
			if op != "on" && op != "off" {
				rstMap["code"] = "-1"
				rstMap["message"] = "非法开关op:" + op
				rb, _ = json.Marshal(rstMap)
				fmt.Fprintf(w, "%s", rb)
				return
			}
			opBool := op == "on" //on为1
			if cid == "all" {
				for confId, _ := range selectConf {
					confIdSwitchMap.Store(confId, opBool)
				}
				rstMap["code"] = "0"
				rstMap["message"] = "已全部为:" + op
				rb, _ = json.Marshal(rstMap)
				fmt.Fprintf(w, "%s", rb)
				log.Printf("web干预:全部策略设为:%s", op)
				return
			} else {
				confId, err := strconv.Atoi(cid)
				if err != nil {
					rstMap["code"] = "-1"
					rstMap["message"] = "cid转换confId(int)异常" + cid
					rb, _ = json.Marshal(rstMap)
					fmt.Fprintf(w, "%s", rb)
					return
				}
				confMaxId := len(selectConf) - 1
				if confId < 0 || confId > confMaxId {
					rstMap["code"] = "-1"
					rstMap["message"] = fmt.Sprintf("非法confId(int):%d,合法范围0~%d", confId, confMaxId)
					rb, _ = json.Marshal(rstMap)
					fmt.Fprintf(w, "%s", rb)
					return
				}
				confIdSwitchMap.Store(confId, opBool)
				rstMap["code"] = "0"
				rstMap["message"] = fmt.Sprintf("%d设为:%s", confId, op)
				rb, _ = json.Marshal(rstMap)
				fmt.Fprintf(w, "%s", rb)
				log.Printf("web干预:策略[%d]设为:%s", confId, op)
				return
			}
		}
		rb, _ = json.Marshal(rstMap)
		fmt.Fprintf(w, "%s", rb)
		return
	}

	rstMap["code"] = "-1"
	rstMap["message"] = "操作未定义"
	rb, _ = json.Marshal(rstMap)
	fmt.Fprintf(w, "%s", rb)
	return
}

func main() {
	//运行参数获取
	flag.Parse()

	//预读数据准备
	dataInit()

	//http服务器初始化
	httpHandle := new(httpEngine)
	server := &http.Server{
		Addr:    *listen,
		Handler: httpHandle,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Println("http服务开启异常：" + err.Error())
			os.Exit(-1)
		}
	}()
	log.Println("http服务已启动，监听地址:", *listen)

	go func() {
		err := hqService(*rePlayFile, hqRbChan)
		if err != nil {
			log.Println("行情接收服务退出:", err)
			stop <- syscall.SIGTERM
		}
		close(hqRbChan)
	}()

	go func() {
		hqParseService()
	}()

	go func() {
		codeHqSetClearService()
	}()

	go func() {
		parseLv1Service()
	}()

	go func() {
		parseLv2Service()
	}()

	go func() {
		orderWatchService()
	}()

	// 等待信号关闭服务器
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// 阻塞直到接收到信号
	sig := <-stop
	log.Println("收到退出信号:", sig)
	// 创建一个5秒的超时上下文
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 优雅关闭服务器，等待所有的活动连接关闭
	log.Println("正在关闭策略...")
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server Shutdown: %v", err)
	}
	log.Println("策略已退出")
}

func orderWatchService() {
	for {
		freshKeyItem()
		time.Sleep(time.Second * 1)
	}
}

func freshKeyItem() {
	url := *tdCenterAddr + "/get?all=1"
	var rb []byte
	var err error
	rb, err = lib.HttpOnce(url, nil, nil, 1000)
	if err != nil {
		log.Println("请求全部keyItem异常", err)
		return
	}
	list := []keyStatusItem{}
	err = json.Unmarshal(rb, &list)
	if err != nil {
		log.Println("解析全部keyItem异常", err)
		return
	}
	//此处只做全局数据同步,不进行操作
	for _, keyItem := range list {
		_, conf, err := parseKeyConf(keyItem.Key)
		if err != nil {
			log.Println("key解析conf异常:", err)
			continue
		}
		syncKeyItem(conf, keyItem)
	}
}

func parseKeyConf(key string) (keyPre string, conf triggerConf, err error) {
	keyEx := strings.Split(key, "@")
	if len(keyEx) != 2 {
		err = fmt.Errorf("非法key,解析失败:", key)
		return
	}
	keyPre = keyEx[0]
	conf = triggerConf{}
	err = json.Unmarshal([]byte(keyEx[1]), &conf)
	if err != nil {
		return
	}
	return
}

//全局持仓数据入口,做买单控制用
func syncKeyItem(conf triggerConf, keyItem keyStatusItem) {
	//更新数据,只读不改,直接传值
	confKeyMap := &sync.Map{}
	if vT, ok := confKeyItemMap.Load(conf); ok {
		confKeyMap = vT.(*sync.Map)
	}
	confKeyMap.Store(keyItem.Key, keyItem)
	confKeyItemMap.Store(conf, confKeyMap)

	//目前持仓全map
	holdCode := map[string]int{}

	confKeyItemMap.Range(func(key, value any) bool {
		conf := key.(triggerConf)
		cnt := 0
		hold := 0
		earn := 0.0

		confKeyMap := value.(*sync.Map)
		confHoldCode := map[string]int{}
		openInfo := &confOpenInfo{}

		if vT, ok := confOpenInfoMap.Load(conf); ok {
			openInfo = vT.(*confOpenInfo)
		}

		confKeyMap.Range(func(key, value any) bool {
			keyItem := value.(keyStatusItem)
			cnt++
			earn += keyItem.Earn
			//目前持仓，买入未全卖出
			if keyItem.BDVolume != 0 && keyItem.BDVolume > keyItem.SDVolume {
				hold++
				confHoldCode[keyItem.Code] = 1
				openInfo.holdMap.Store(keyItem.Code, 1)

				holdCode[keyItem.Code] = 1
				allHoldCodeMap.Store(keyItem.Code, keyItem) //止盈止损需code索引,查item
			}
			return true
		})

		//清除多余
		openInfo.holdMap.Range(func(key, value any) bool {
			code := key.(string)
			if _, ok := confHoldCode[code]; !ok {
				openInfo.holdMap.Delete(code)
			}
			return true
		})

		openInfo.cnt = cnt
		openInfo.hold = hold
		openInfo.earn = math.Round(earn*100) / 100
		//回写
		confOpenInfoMap.Store(conf, openInfo)
		return true
	})

	//清除多余
	allHoldCodeMap.Range(func(key, value any) bool {
		code := key.(string)
		if _, ok := holdCode[code]; !ok {
			allHoldCodeMap.Delete(code)
		}
		return true
	})

}

func dealCb(keyItem keyStatusItem) {
	keyPre, conf, err := parseKeyConf(keyItem.Key)
	if err != nil {
		log.Println("key解析异常,跳过:", err)
		return
	}
	//只接受 全成/全撤/部撤 三种退出条件的回调
	if !(keyItem.BStatus == trade.O_STATUS_DONE || keyItem.BStatus == trade.O_STATUS_CANCEL || keyItem.BStatus == trade.O_STATUS_PART_CANCEL) {
		log.Printf("%s异常回调,跳过:%s,%+v", keyPre, keyItem.BStatus, keyItem)
		return
	}

	//打板模式为T+1,当日只处理买入回调
	action := "卖出"
	status := keyItem.SStatus
	if keyItem.SStatus == "" { //无卖状态，即为买结束回调。买成买撤都将清除临时标志位
		action = "买入"
		status = keyItem.BStatus
		//清除临时占位
		openTmp := &sync.Map{}
		if vT, ok := confOpenTmpMap.Load(conf); ok {
			openTmp = vT.(*sync.Map)
			openTmp.Range(func(key, value any) bool {
				code := key.(string)
				confKey := value.(string)
				if confKey == keyItem.Key {
					openTmp.Delete(code)
					confOpenTmpMap.Store(conf, openTmp)
				}
				return true
			})
		}
	}
	//主动同步持仓情况
	syncKeyItem(conf, keyItem)
	if action == "买入" {
		if keyItem.BDVolume != 0 {
			log.Printf("%s%s%s,%s,%s,价格:%.2f,数量:%.f", keyPre, action, status, keyItem.Name, keyItem.Code, keyItem.BDPrice, keyItem.BDVolume)
		} else {
			log.Printf("%s%s%s,%s,%s,数量:%.f", keyPre, action, status, keyItem.Name, keyItem.Code, keyItem.BOVolume)
		}
	} else {
		if keyItem.SDVolume != 0 {
			earn := math.Round(keyItem.SDVolume*(keyItem.SDPrice-keyItem.BDPrice)*100) / 100
			log.Printf("%s%s%s,%s,%s,价格:%.2f,数量:%.f,earn:%.2f", keyPre, action, status, keyItem.Name, keyItem.Code, keyItem.SDPrice, keyItem.SDVolume, earn)
		} else {
			log.Printf("%s%s%s,%s,%s,数量:%.f", keyPre, action, status, keyItem.Name, keyItem.Code, keyItem.SOVolume)
		}
	}
}

func dataInit() {
	//加载流通股映射
	stockSharesMap, err = readStockSharesMap(*sharesFile)
	if err != nil {
		log.Fatalln("读取流通股映射文件错误", *sharesFile, err)
	}

	//读取触发条件
	selectConf, err = readTriggerConf(*confFile)
	if err != nil {
		log.Fatalln("读取触发条件配置错误", *confFile, err)
	}
}

func hqService(file string, ch chan []byte) (err error) {
	url := *hqCenterAddr + "/hq"
	if file != "" {
		url = url + "?file=" + file
	}
	err = lib.SseGet(url, ch)
	return
}

//lv2行情暂未接入
func parseLv2Service() {
	for s := range lv2RawChan {
		_ = s
		//log.Println("lv2",s)
	}
}
func parseLv1Service() {
	for s := range lv1RawChan {
		sEx := strings.Split(s, ",")
		if len(sEx) != 27 {
			continue
		}
		var (
			code      = sEx[0]
			timeStr   = sEx[1]
			name      = sEx[2]
			priceStr  = sEx[3]
			ratioStr  = sEx[4]
			amountStr = sEx[5]
			volumeStr = sEx[6]
		)

		stamp, _ := lib.TimeToStamp(date+" "+timeStr, "", "")

		//正式开盘前数据丢弃
		if timeStr < morningStart {
			continue
		}

		//价格和成交额没变，则无需触发
		uniqKeyArr := []string{code, timeStr, priceStr, amountStr}
		uniqKey := strings.Join(uniqKeyArr, "_")
		//已处理过,跳过
		if _, ok := codeHqSet.Load(uniqKey); ok {
			continue
		}

		//记录首次收到该行情时间戳,定时清理,避免内存占用
		//codeHqSet.Store(uniqKey, time.Now().Unix())

		var (
			price, _  = strconv.ParseFloat(priceStr, 64)
			ratio, _  = strconv.ParseFloat(ratioStr, 64)
			volume, _ = strconv.ParseFloat(volumeStr, 64)
			amount, _ = strconv.ParseFloat(amountStr, 64)
			b1, _     = strconv.ParseFloat(sEx[7], 64)
			b1p, _    = strconv.ParseFloat(sEx[8], 64)
			b2, _     = strconv.ParseFloat(sEx[9], 64)
			b2p, _    = strconv.ParseFloat(sEx[10], 64)
			b3, _     = strconv.ParseFloat(sEx[11], 64)
			b3p, _    = strconv.ParseFloat(sEx[12], 64)
			b4, _     = strconv.ParseFloat(sEx[13], 64)
			b4p, _    = strconv.ParseFloat(sEx[14], 64)
			b5, _     = strconv.ParseFloat(sEx[15], 64)
			b5p, _    = strconv.ParseFloat(sEx[16], 64)
			s1, _     = strconv.ParseFloat(sEx[17], 64)
			s1p, _    = strconv.ParseFloat(sEx[18], 64)
			s2, _     = strconv.ParseFloat(sEx[19], 64)
			s2p, _    = strconv.ParseFloat(sEx[20], 64)
			s3, _     = strconv.ParseFloat(sEx[21], 64)
			s3p, _    = strconv.ParseFloat(sEx[22], 64)
			s4, _     = strconv.ParseFloat(sEx[23], 64)
			s4p, _    = strconv.ParseFloat(sEx[24], 64)
			s5, _     = strconv.ParseFloat(sEx[25], 64)
			s5p, _    = strconv.ParseFloat(sEx[26], 64)
		)

		hqMap := lv1HqMap{timeStr, code, name, price, ratio, volume, amount, stamp, 0.0, b1, b1p, b2, b2p, b3, b3p, b4, b4p, b5, b5p, s1, s1p, s2, s2p, s3, s3p, s4, s4p, s5, s5p}

		if shares, ok := stockSharesMap[code]; ok {
			hqMap.turnover = volume / shares * 100
		}
		codeLatestHqMap.Store(code, hqMap)

		//行情队列处理
		hqList := []lv1HqMap{}
		if oldT, ok := codeHqWindow.Load(code); ok {
			hqList = oldT.([]lv1HqMap)
		}

		//行情队列输入策略
		if price > 4 {
			selectStock(code, hqMap, hqList)
		}

		//行情队列长度控制
		hqList = append(hqList, hqMap)
		size := len(hqList)
		if size > lv1HqWindowLen { //保留确定长度的队列，减小内存消耗
			hqList = hqList[size-lv1HqWindowLen:]
		}
		codeHqWindow.Store(code, hqList) //进队列保存
	}
}
func hqParseService() {
	for rb := range hqRbChan {
		ex1 := strings.Split(string(rb), "\n")
		for _, s := range ex1 {
			ex2 := strings.Split(s, "=")
			if len(ex2) != 2 {
				continue
			}
			code := ex2[0]
			info := ex2[1]
			if strings.HasPrefix(code, "lv1_") {
				code = strings.ReplaceAll(code, "lv1_", "")
				lv1RawChan <- code + "," + info
			}
			if strings.HasPrefix(code, "lv2_") {
				code = strings.ReplaceAll(code, "lv2_", "")
				lv2RawChan <- code + "," + info
			}
		}
	}
}
func codeHqSetClearService() {
	for {
		codeHqSet.Range(func(key, value any) bool {
			k := key.(string)
			t := value.(time.Time)
			if time.Since(t) > time.Second*10 {
				codeHqSet.Delete(k)
			}
			return true
		})
		time.Sleep(time.Second * 10)
	}
}

func selectStock(code string, latest lv1HqMap, hqList []lv1HqMap) {
	size := len(hqList)
	for confId, conf := range selectConf {
		confOn := 1
		if vT, ok := confIdSwitchMap.Load(confId); ok {
			isOn := vT.(bool)
			if !isOn {
				confOn = 0
			}
		}
		if confOn == 0 { //关闭
			continue
		}

		//入队列时间倒序处理
		for i := size - 1; i >= 0; i-- {
			backHq := hqList[i]
			raDiff := latest.ratio - backHq.ratio
			tnDiff := latest.turnover - backHq.turnover
			timeDiff := latest.stamp - backHq.stamp
			amountDiff := (latest.amount - backHq.amount) / 10000
			amountPerSec := amountDiff / timeDiff
			//触发条件
			if latest.ratio >= conf.RaTh && timeDiff > conf.Sec-3 && timeDiff <= conf.Sec+3 && raDiff/timeDiff >= conf.RaRate && tnDiff/timeDiff >= conf.TnRate && amountPerSec >= conf.StockAmt {
				log.Printf("[%d]打板触发:%s,%s,%.2f,时间:%s~%s,%.f秒,涨幅:%.2f,换手:%.2f,成交额:%.f万,%.1f万/秒", confId, latest.name, code, latest.ratio, backHq.time, latest.time, timeDiff, raDiff, tnDiff, amountDiff, amountPerSec)
				go buyCtl(confId, code, conf)
				break
			}
		}
	}
}

func buyCtl(confId int, code string, conf triggerConf) {

	confJs, _ := json.Marshal(conf) //配置结构体转字符串做key

	price := 0.0
	hqMap := lv1HqMap{}
	if vT, ok := codeLatestHqMap.Load(code); ok {
		hqMap = vT.(lv1HqMap)
		//优先用卖一价
		price = hqMap.b1p
		if hqMap.s1p != 0 {
			price = hqMap.s1p
		}
	}

	if price == 0.0 {
		log.Printf("[%d]买入跳过:%s,%s,未获取到最近价格信息:", confId, hqMap.name, code)
		return
	}

	openTmp := &sync.Map{}
	if vT, ok := confOpenTmpMap.Load(conf); ok {
		openTmp = vT.(*sync.Map)
	}
	_, exist := openTmp.Load(code)
	tmpCnt := 0
	openTmp.Range(func(key, value any) bool {
		tmpCnt++
		return true
	})

	confHoldInfo := &confOpenInfo{}
	if vT, ok := confOpenInfoMap.Load(conf); ok {
		confHoldInfo = vT.(*confOpenInfo)
		if _, ok := confHoldInfo.holdMap.Load(code); ok {
			exist = true
		}
	}

	if exist {
		log.Printf("[%d]买入跳过:%s,%s,该股同条件下已有买单在处理", confId, hqMap.name, code)
		return
	}

	if confHoldInfo.hold+tmpCnt >= int(conf.HoldCnt) {
		log.Printf("[%d]买入跳过:%s,%s,持仓数(%d)已达条件上限(%.f)", confId, hqMap.name, code, confHoldInfo.hold+tmpCnt, conf.HoldCnt)
		return
	}

	vol := conf.Vol
	if vol == 0 { //未指定买入手数，按金额定
		vol = getProperVol(conf.Amt, price)
	}

	//[conf发单计数][槽位号][confId]|@conf
	keyPre := fmt.Sprintf("[%d][%d][%d]|", confHoldInfo.cnt+tmpCnt, confHoldInfo.hold+tmpCnt, confId)
	key := keyPre + "@" + string(confJs)
	priceStr := fmt.Sprintf("%.2f", price)
	timeoutStr := fmt.Sprintf("%f", conf.BWait)
	volStr := strconv.Itoa(int(vol))
	params := url.Values{
		"key":     []string{key},
		"code":    []string{code},
		"name":    []string{hqMap.name},
		"price":   []string{priceStr},
		"vol":     []string{volStr},
		"cb":      []string{*localCbAddr},
		"timeout": []string{timeoutStr},
	}
	if vol == 0 {
		log.Printf("%s买入跳过:%s,%s,买单价格:%s,资金配额:%.f,配额不足,买量为0,跳过", keyPre, hqMap.name, code, priceStr, conf.Amt)
		return
	}

	//回放模式不进行交易
	if *rePlayFile != "" {
		log.Printf("%s回放买债:%s,%s,价格:%s,数量:%s", keyPre, hqMap.name, code, priceStr, volStr)
		return
	}

	//io请求前先占位
	openTmp.Store(code, key)
	confOpenTmpMap.Store(conf, openTmp)

	rsp := trade.TradeRsp{}
	url := *tdCenterAddr + "/buy?" + params.Encode()
	rb, err := lib.HttpOnce(url, nil, nil, 1000)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	err = json.Unmarshal(rb, &rsp)
	if err != nil {
		msg = err.Error()
	}
	if rsp.Message != "" {
		msg = rsp.Message
	}
	if rsp.OrderId == "" { //以是否获取到单号来判断挂单是否成功
		log.Printf("%s买单异常:%s,%s,价格:%s,数量:%s,异常:%s", keyPre, hqMap.name, code, priceStr, volStr, msg)
		//未成,解除占位
		openTmp.Delete(code)
		confOpenTmpMap.Store(conf, openTmp)
		return
	}
	log.Printf("%s买单发出:%s,%s,价格:%s,数量:%s,单号:%s", keyPre, hqMap.name, code, priceStr, volStr, rsp.OrderId)
}

func getProperVol(amt, price float64) float64 {
	if price == 0 {
		return 0
	}
	return math.Floor(amt/100/price) * 100
}

func readTriggerConf(path string) (confs []triggerConf, err error) {
	cfh, err := os.Open(path)
	if err != nil {
		return
	}
	cfc, err := ioutil.ReadAll(cfh)
	if err != nil {
	}
	err = json.Unmarshal(cfc, &confs)
	if err != nil {
	}
	log.Printf("条件配置:\n%+s", cfc)
	log.Printf("条件配置格式化:\n%+v", confs)
	return
}

func readStockSharesMap(path string) (bs map[string]float64, err error) {
	cfh, err := os.Open(path)
	if err != nil {
		return
	}
	cfc, err := ioutil.ReadAll(cfh)
	if err != nil {
		return bs, err
	}
	err = json.Unmarshal(cfc, &bs)
	if err != nil {
		return bs, err
	}
	return
}
