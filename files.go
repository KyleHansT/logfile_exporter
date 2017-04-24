package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/howeyc/fsnotify"
	"github.com/hpcloud/tail"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
)

var (
	//live日志
	AllMessageMap map[string]*LogStat
	ScrapeMap     map[string]*LogStat
	CollectorMap  map[string]*LogStat
	MutexMap      *sync.Mutex
	LiveSystem    = "live"

	//web日志
	WebAllMessageMap map[string]*LogStat
	WebScrapeMap     map[string]*LogStat
	WebCollectorMap  map[string]*LogStat
	WebMutexMap      *sync.Mutex
	WebSystem        = "web"

	TrimReg    *regexp.Regexp
	repalceReg = " "
)

var (
	Type_Req       = "req"
	Type_Resp      = "resp"
	Type_Grpc      = "grpc"
	Type_Broadcast = "broadcast"
	Type_Http      = "http"
)

var (
	requestDurationDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, LiveSystem, "request_duration_seconds"),
		"avg duration of the request during seconds.",
		[]string{"message_name", "message_id", "type"}, nil,
	)
	requestCountDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, LiveSystem, "request_number"),
		"avg count of the request during seconds.",
		[]string{"message_name", "message_id", "type"}, nil,
	)
	packageSizeDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, LiveSystem, "package_size_bytes"),
		"avg package size of the request response during seconds.",
		[]string{"message_name", "message_id", "type"}, nil,
	)
	broadcastCountDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, LiveSystem, "broadcast_number"),
		"avg count of the broadcast to users during seconds.",
		[]string{"message_name", "message_id", "type"}, nil,
	)

	//web 日志
	webRequestDurationDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, WebSystem, "request_duration_seconds"),
		"avg duration of the request during seconds.",
		[]string{"message_name", "type"}, nil,
	)
	webRequestCountDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, WebSystem, "request_number"),
		"avg count of the request during seconds.",
		[]string{"message_name", "type"}, nil,
	)
	webPackageSizeDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, WebSystem, "package_size_bytes"),
		"avg package size of the request response during seconds.",
		[]string{"message_name", "type"}, nil,
	)
)

type LogStat struct {
	MessageId      string  //消息ID
	MessageName    string  //消息名称或接口名称
	RequestCount   int     //接口请求次数
	AverageTimeUs  float64 //请求平均执行时间us
	AveragePkSize  int     //平均数据包大小
	BroadcastCount int     //总广播用户数
	Type           string  //请求类型
}

func walkLogDir(dir string) (files []string, err error) {
	if string(dir[len(dir)-1]) != "/" {
		dir = dir + "/"
	}
	visit := func(path string, f os.FileInfo, err error) error {
		if f.IsDir() {
			return nil
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil
		}
		files = append(files, abs)
		return nil
	}
	err = filepath.Walk(dir, visit)
	return
}

func WatchFiles(dirs []string) {
	CollectorMap = map[string]*LogStat{}
	AllMessageMap = map[string]*LogStat{}

	WebCollectorMap = map[string]*LogStat{}
	WebAllMessageMap = map[string]*LogStat{}

	// get list of all files in watch dir
	files := make([]string, 0)
	for _, dir := range dirs {
		fs, err := walkLogDir(dir)
		if err != nil {
			panic(err)
		}
		for _, f := range fs {
			files = append(files, f)
		}
	}

	// assign file per group
	assignedFiles, err := assignFiles(files)
	if err != nil {
		log.Fatal(err)
	}

	doneCh := make(chan string)
	for _, file := range assignedFiles {
		file.doneCh = doneCh
		go file.tail()
	}

	for _, dir := range dirs {
		go continueWatch(&dir, doneCh)
	}

	for {
		select {
		case fpath := <-doneCh:
			log.Infof("finished reading file: %v", fpath)
		}
	}

}

func assignFiles(allFiles []string) (outFiles []*File, err error) {
	files := make([]*File, 0)
	log.Infoln("files:", allFiles)
	liveReg, _ := regexp.Compile(`[Ll]ive[^/]*log$`)
	webReg, _ := regexp.Compile(`[Ww]eb[^/]*log$`)
	for _, f := range allFiles {
		if strings.Contains(f[len(*watchPath):], "/") {
			continue
		}
		if liveReg.MatchString(f) {
			file, err := NewFile(f, "live")
			if err != nil {
				return files, err
			}
			log.Infof("match file: %s  filetype: %s", f, file.FileType)
			files = append(files, file)
		}
		if webReg.MatchString(f) {
			file, err := NewFile(f, "web")
			if err != nil {
				return files, err
			}
			log.Infof("match file: %s  filetype: %s", f, file.FileType)
			files = append(files, file)
		}
	}
	return files, nil
}

func continueWatch(dir *string, doneCh chan string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}

	done := make(chan bool)

	// Process events
	go func() {
		for {
			select {
			case ev := <-watcher.Event:
				if ev.IsCreate() {
					files := make([]string, 0)
					file, err := filepath.Abs(ev.Name)
					if err != nil {
						log.Errorf("can't get file %+v", err)
						continue
					}

					files = append(files, file)
					tailFiles, _ := assignFiles(files)
					for _, tailFile := range tailFiles {
						log.Infof("continueWatch file: %v", tailFile.Tail.Filename)
						tailFile.doneCh = doneCh
						go tailFile.tail()
					}
				}
			case err := <-watcher.Error:
				log.Error("error:", err)
			}
		}
	}()

	err = watcher.Watch(*dir)
	if err != nil {
		log.Fatal(err)
	}

	<-done

	/* ... do stuff ... */
	watcher.Close()
}

func NewFile(fpath, filetype string) (*File, error) {
	file := &File{}
	var err error
	file.FileType = filetype
	if *seek == "set" {
		file.Tail, err = tail.TailFile(fpath, tail.Config{Follow: true})
	} else {
		seekInfo := &tail.SeekInfo{Offset: 0, Whence: os.SEEK_END}
		file.Tail, err = tail.TailFile(fpath, tail.Config{Follow: true, Location: seekInfo})
	}

	return file, err
}

type File struct {
	Tail     *tail.Tail
	doneCh   chan string
	FileType string
}

func (self *File) tail() {
	log.Infoln("start tailing %v", self.Tail.Filename)
	defer func() { self.doneCh <- self.Tail.Filename }()

	for line := range self.Tail.Lines {
		if line.Text == "" {
			continue
		}

		switch self.FileType {
		case "live":
			analysisLiveLog(line.Text)
		case "web":
			analysisWebLog(line.Text)
		}
	}
}

func analysisLiveLog(line string) {
	MutexMap.Lock() //用于统计不同时间区间切换缓存的锁
	defer MutexMap.Unlock()
	lineContent := TrimReg.ReplaceAllString(line, repalceReg)
	contents := strings.Split(lineContent, " ")
	lenght := len(contents)
	switch {
	//userSession.go部分的日志处理
	case lenght > 10 && contents[5] == ">>>>>>(Json)": //返回Resp
		rs := []rune(contents[8])
		MessageId := string(rs[3:])

		rs = []rune(contents[9])
		size, _ := strconv.Atoi(string(rs[5:]))

		if stat, ok := CollectorMap[contents[6]]; ok {
			stat.RequestCount += 1
			stat.AveragePkSize += size
		} else {
			statTmp := &LogStat{
				MessageId:     MessageId,
				MessageName:   contents[6],
				RequestCount:  1,
				AveragePkSize: size,
				Type:          Type_Resp,
			}
			CollectorMap[contents[6]] = statTmp
		}
	case lenght > 10 && contents[5] == "<<<<<<(Json)": //请求Req
		rs := []rune(contents[8])
		MessageId := string(rs[3:])

		if stat, ok := CollectorMap[contents[6]]; ok {
			stat.RequestCount += 1
		} else {
			statTmp := &LogStat{
				MessageId:    MessageId,
				MessageName:  contents[6],
				RequestCount: 1,
				Type:         Type_Req,
			}
			CollectorMap[contents[6]] = statTmp
		}
	case lenght > 7 && strings.Contains(contents[4], "userSession.go") && contents[5] == "finish": //请求Req结束
		ReqNames := strings.Split(contents[6], ",")
		if len(ReqNames) >= 1 {
			if stat, ok := CollectorMap[ReqNames[0]]; ok {
				rs := []rune(contents[7])
				USecond, _ := strconv.ParseFloat(string(rs[3:]), 64)
				stat.AverageTimeUs += USecond
			}
		}

	//grpc调用日志处理
	case lenght > 10 && contents[5] == "invoke" && contents[6] == "rpc": //服务端grpc调用
		rs := []rune(contents[9])
		USecond, _ := strconv.ParseFloat(string(rs[3:]), 64)

		strs := strings.Split(contents[8], "Proto.")
		if len(strs) == 2 {
			rpcName := strs[1]
			if stat, ok := CollectorMap[rpcName]; ok {
				stat.RequestCount += 1
				stat.AverageTimeUs += USecond
			} else {
				statTmp := &LogStat{
					MessageName:   rpcName,
					RequestCount:  1,
					AverageTimeUs: USecond,
					Type:          Type_Grpc,
				}
				CollectorMap[rpcName] = statTmp
			}
		}

	//notify日志的处理
	case lenght > 7 && contents[4] == "broadcast" && contents[5] == "finish":
		rs := []rune(contents[7])
		NotifyName := string(rs[5:])

		rs = []rune(contents[8])
		SendCount, _ := strconv.Atoi(string(rs[10:]))

		rs = []rune(contents[9])
		USecond, _ := strconv.ParseFloat(string(rs[3:]), 64)

		if stat, ok := CollectorMap[NotifyName]; ok {
			stat.RequestCount += 1
			stat.BroadcastCount += SendCount
			stat.AverageTimeUs += USecond
		} else {
			statTmp := &LogStat{
				MessageName:    NotifyName,
				RequestCount:   1,
				BroadcastCount: SendCount,
				AverageTimeUs:  USecond,
				Type:           Type_Broadcast,
			}
			CollectorMap[NotifyName] = statTmp
		}
	}
}

func analysisWebLog(line string) {
	WebMutexMap.Lock() //用于统计不同时间区间切换缓存的锁
	defer WebMutexMap.Unlock()

	lineContent := TrimReg.ReplaceAllString(line, repalceReg)
	contents := strings.Split(lineContent, " ")
	lenght := len(contents)
	switch {
	case lenght >= 9 && contents[4] == "invoke" && contents[5] == "rpc": //web服务grpc调用
		rs := []rune(contents[8])
		USecond, _ := strconv.ParseFloat(string(rs[3:]), 64)

		strs := strings.Split(contents[7], "Proto.")
		if len(strs) == 2 {
			rpcName := strs[1]
			if stat, ok := WebCollectorMap[rpcName]; ok {
				stat.RequestCount += 1
				stat.AverageTimeUs += USecond
			} else {
				statTmp := &LogStat{
					MessageName:   rpcName,
					RequestCount:  1,
					AverageTimeUs: USecond,
					Type:          Type_Grpc,
				}
				WebCollectorMap[rpcName] = statTmp
			}
		}

	case lenght >= 14 && contents[0] == "[GIN]": //web服务get、post请求
		rs := []rune(contents[7])
		USecond := float64(0)
		unitOfTime := string(rs[len(rs)-2:])
		if unitOfTime == "ms" {
			USecond, _ = strconv.ParseFloat(string(rs[:len(rs)-2]), 64)
			USecond *= 1000
		} else {
			us, _ := strconv.Atoi(string(rs[:len(rs)-2]))
			USecond = float64(us)
		}
		webReqName := contents[13] //注非12，因为日志内包含特殊符号在get之前

		if stat, ok := WebCollectorMap[webReqName]; ok {
			stat.RequestCount += 1
			stat.AverageTimeUs += USecond
		} else {
			statTmp := &LogStat{
				MessageName:   webReqName,
				RequestCount:  1,
				AverageTimeUs: USecond,
				Type:          Type_Http,
			}
			WebCollectorMap[webReqName] = statTmp
		}
	}
}

func ScrapeDatas(ch chan<- prometheus.Metric) error {
	MutexMap.Lock()
	defer MutexMap.Unlock()

	//切换缓存，进行下个时间区间的统计
	ScrapeMap = CollectorMap
	CollectorMap = map[string]*LogStat{}
	log.Infoln("ScrapeMap size:", len(ScrapeMap))
	for _, stat := range ScrapeMap {
		if stat.RequestCount > 0 {
			ch <- prometheus.MustNewConstMetric(
				requestCountDesc, prometheus.GaugeValue, float64(stat.RequestCount),
				stat.MessageName, stat.MessageId, stat.Type)
		}
		if stat.AverageTimeUs > 0 && stat.RequestCount > 0 {
			ch <- prometheus.MustNewConstMetric(
				requestDurationDesc, prometheus.GaugeValue, stat.AverageTimeUs/float64(stat.RequestCount)/float64(1000000),
				stat.MessageName, stat.MessageId, stat.Type)
		}
		if stat.AveragePkSize > 0 && stat.RequestCount > 0 {
			ch <- prometheus.MustNewConstMetric(
				packageSizeDesc, prometheus.GaugeValue, float64(stat.AveragePkSize/stat.RequestCount),
				stat.MessageName, stat.MessageId, stat.Type)
		}
		if stat.BroadcastCount > 0 {
			ch <- prometheus.MustNewConstMetric(
				broadcastCountDesc, prometheus.GaugeValue, float64(stat.BroadcastCount),
				stat.MessageName, stat.MessageId, stat.Type)
		}

		AllMessageMap[stat.MessageName] = stat
	}

	//未触发的请求置零
	for MessageName, stat := range AllMessageMap {
		if _, ok := ScrapeMap[MessageName]; !ok {
			ch <- prometheus.MustNewConstMetric(requestCountDesc, prometheus.GaugeValue, float64(0),
				MessageName, stat.MessageId, stat.Type)
			if stat.Type != Type_Resp {
				ch <- prometheus.MustNewConstMetric(requestDurationDesc, prometheus.GaugeValue, float64(0),
					MessageName, stat.MessageId, stat.Type)
			}
			if stat.Type == Type_Resp {
				ch <- prometheus.MustNewConstMetric(packageSizeDesc, prometheus.GaugeValue, float64(0),
					MessageName, stat.MessageId, stat.Type)
			}
			if stat.Type == Type_Broadcast {
				ch <- prometheus.MustNewConstMetric(broadcastCountDesc, prometheus.GaugeValue, float64(0),
					MessageName, stat.MessageId, stat.Type)
			}
			delete(AllMessageMap, MessageName)
		}
	}

	//清空抓取缓存
	ScrapeMap = map[string]*LogStat{}

	return nil
}

func ScrapeWebDatas(ch chan<- prometheus.Metric) error {
	WebMutexMap.Lock()
	defer WebMutexMap.Unlock()

	//切换缓存，进行下个时间区间的统计
	WebScrapeMap = WebCollectorMap
	WebCollectorMap = map[string]*LogStat{}
	log.Infoln("WebScrapeMap size:", len(WebScrapeMap))
	for _, stat := range WebScrapeMap {
		if stat.RequestCount > 0 {
			ch <- prometheus.MustNewConstMetric(
				webRequestCountDesc, prometheus.GaugeValue, float64(stat.RequestCount), stat.MessageName, stat.Type,
			)
		}
		if stat.AverageTimeUs > 0 && stat.RequestCount > 0 {
			ch <- prometheus.MustNewConstMetric(
				webRequestDurationDesc, prometheus.GaugeValue, stat.AverageTimeUs/float64(stat.RequestCount)/float64(1000000),
				stat.MessageName, stat.Type)
		}
		WebAllMessageMap[stat.MessageName] = stat
	}

	//未触发的请求置零
	for MessageName, stat := range WebAllMessageMap {
		if _, ok := WebScrapeMap[MessageName]; !ok {
			ch <- prometheus.MustNewConstMetric(webRequestCountDesc, prometheus.GaugeValue, float64(0),
				MessageName, stat.Type)
			ch <- prometheus.MustNewConstMetric(webRequestDurationDesc, prometheus.GaugeValue, float64(0),
				MessageName, stat.Type)
			delete(WebAllMessageMap, MessageName)
		}
	}

	//清空抓取缓存
	WebScrapeMap = map[string]*LogStat{}

	return nil
}
