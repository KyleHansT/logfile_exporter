# logfile_exporter

非通用的工具、日志分析逻辑需要自己编写代码实现

"github.com/hpcloud/tail" 这个开源库有点问题，需要在waitForChange函数里调用cleanup()函数。否则若日志文件被重建tail -f功能失效
