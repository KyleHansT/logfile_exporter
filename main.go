package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/version"
	"gopkg.in/ini.v1"
)

var (
	showVersion = flag.Bool(
		"version", false,
		"Print version information.",
	)
	metricPath = flag.String(
		"web.telemetry-path", "/metrics",
		"Path under which to expose metrics.",
	)
	watchPath = flag.String(
		"watch_path", "./",
		"Path of log file to analysis.",
	)
	
	seek = flag.String(
		"seek", "end",
		"seek to the file",
	)
	
	listenAddress = flag.String(
		"web.listen-address", ":9191",
		"Address to listen on for web interface and telemetry.",
	)
)

// Metric name parts.
const (
	// Namespace for all metrics.
	namespace = "logfile"
	// Subsystem(s).
	exporter = "exporter"
)

// landingPage contains the HTML served at '/'.
// TODO: Make this nicer and more informative.
var landingPage = []byte(`<html>
<head><title>logefile exporter</title></head>
<body>
<h1>logefile exporter</h1>
<p><a href='` + *metricPath + `'>Metrics</a></p>
</body>
</html>
`)

// Exporter collects logfile metrics. It implements prometheus.Collector.
type Exporter struct {
	logpath  string
	duration prometheus.Gauge
}

// NewExporter returns a new logfile exporter for the provided path.
func NewExporter(logpath string) *Exporter {
	return &Exporter{
		logpath: logpath,
		duration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "last_scrape_duration_seconds",
			Help:      "Duration of the last scrape of metrics from MySQL.",
		}),
	}
}

// Describe implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	metricCh := make(chan prometheus.Metric)
	doneCh := make(chan struct{})

	go func() {
		for m := range metricCh {
			ch <- m.Desc()
		}
		close(doneCh)
	}()

	e.Collect(metricCh)
	close(metricCh)
	<-doneCh
}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.scrape(ch)
	ch <- e.duration
}

func (e *Exporter) scrape(ch chan<- prometheus.Metric) {
	defer func(begun time.Time) {
		e.duration.Set(time.Since(begun).Seconds())
	}(time.Now())

	ScrapeDatas(ch)
	ScrapeWebDatas(ch)
}

func parseMycnf(config interface{}) (string, error) {
	cfg, err := ini.Load(config)
	if err != nil {
		log.Errorln(err)
		return "", err
	}
	path := cfg.Section("logfile").Key("watch_path").String()
	log.Infoln("path:", path)
	return path, nil
}

func init() {
	prometheus.MustRegister(version.NewCollector("logfile_exporter"))
}

func main() {
	flag.Parse()

	if *showVersion {
		fmt.Fprintln(os.Stdout, version.Print("logfile_exporter"))
		os.Exit(0)
	}
	if string((*watchPath)[len(*watchPath)-1]) != "/" {
		*watchPath = *watchPath + "/"
	}
	log.Infoln("Starting logfile_exporter", version.Info())
	log.Infoln("Build context", version.BuildContext())
	log.Infoln("watchPath:", watchPath)
	
	MutexMap = new(sync.Mutex)
	WebMutexMap = new(sync.Mutex)
	TrimReg = regexp.MustCompile(`\s+`)

	go WatchFiles([]string{*watchPath})

	exporter := NewExporter(*watchPath)
	prometheus.MustRegister(exporter)

	http.Handle(*metricPath, prometheus.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(landingPage)
	})

	log.Infoln("Listening on", *listenAddress)
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
