# logfile_exporter
log file exporter to prometheus

"github.com/hpcloud/tail" 这个开源库有点问题，需要在waitForChange函数里调用cleanup()函数。否则若日志文件被重建tail -f功能失效
