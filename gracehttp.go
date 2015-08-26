package gracehttp

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// refer http.ListenAndServe
func ListenAndServe(addr string, handler http.Handler) error {
	return newGraceHttp(addr, handler).ListenAndServe()
}

// refer http.ListenAndServeTLS
func ListenAndServeTLS(addr string, certFile string, keyFile string, handler http.Handler) error {
	return newGraceHttp(addr, handler).ListenAndServeTLS(certFile, keyFile)
}

// new grace http
func newGraceHttp(addr string, handler http.Handler) *GraceHttp {

	// 解析命令行参数
	var isGraceful bool

	flag.BoolVar(&isGraceful, "graceful", false, "graceful restart http application")
	flag.Parse()

	// 实例化GraceHttp
	return &GraceHttp{
		Server: &http.Server{
			Addr:    addr,
			Handler: handler,
		},

		isGraceful: isGraceful,
		signalChan: make(chan os.Signal),
	}
}

// 支持优雅重启的http服务
type GraceHttp struct {
	Server   *http.Server
	listener net.Listener

	isGraceful bool
	signalChan chan os.Signal
}

func (this *GraceHttp) ListenAndServe() error {
	addr := this.Server.Addr
	if addr == "" {
		addr = ":http"
	}

	ln, err := this.getNetListener(addr)
	if err != nil {
		return err
	}

	this.listener = newListener(ln.(*net.TCPListener))

	return this.Serve()
}

func (this *GraceHttp) ListenAndServeTLS(certFile, keyFile string) error {
	addr := this.Server.Addr
	if addr == "" {
		addr = ":https"
	}

	config := &tls.Config{}
	if this.Server.TLSConfig != nil {
		*config = *this.Server.TLSConfig
	}
	if config.NextProtos == nil {
		config.NextProtos = []string{"http/1.1"}
	}

	var err error
	config.Certificates = make([]tls.Certificate, 1)
	config.Certificates[0], err = tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}

	ln, err := this.getNetListener(addr)
	if err != nil {
		return err
	}

	this.listener = tls.NewListener(newListener(ln.(*net.TCPListener)), config)
	return this.Serve()
}

func (this *GraceHttp) Serve() error {

	// 处理信号
	go this.handleSignals()

	// 处理HTTP请求
	err := this.Server.Serve(this.listener)

	// 跳出Serve处理代表 listener 已经close，等待所有已有的连接处理结束
	this.listener.(*Listener).Wait()

	return err
}

func (this *GraceHttp) getNetListener(addr string) (ln net.Listener, err error) {

	if this.isGraceful {
		file := os.NewFile(3, "")
		ln, err = net.FileListener(file)
		if err != nil {
			err = fmt.Errorf("net.FileListener error: %v", err)
			return
		}
	} else {
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			err = fmt.Errorf("net.Listen error: %v", err)
			return
		}
	}
	return
}

func (this *GraceHttp) handleSignals() {
	var sig os.Signal

	signal.Notify(
		this.signalChan,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
	)

	pid := os.Getpid()
	for {
		sig = <-this.signalChan

		switch sig {

		case syscall.SIGINT:

			this.logf("pid %d received SIGINT.", pid)
			this.logf("graceful shutting down http server...")
			this.shutdown()

		case syscall.SIGTERM:

			this.logf("pid %d received SIGTERM.", pid)
			this.logf("graceful shutting down http server...")
			this.shutdown()

		case syscall.SIGHUP:

			this.logf("pid %d received SIGHUP.", pid)
			this.logf("graceful restart http server...")

			err := this.fork()
			if err != nil {
				this.logf("fork error: %v.", err)
			}

		default:

		}
	}
}

func (this *GraceHttp) shutdown() {

	// 通过设置超时使得进程不再接受新请求
	this.listener.(*Listener).SetDeadline(time.Now())

	// 关闭链接
	this.listener.(*Listener).Close()
}

func (this *GraceHttp) fork() error {

	// 启动子进程，并执行新程序

	listenerFd, err := this.listener.(*Listener).GetFd()
	if err != nil {
		return fmt.Errorf("failed to get socket file descriptor: %v.", err)
	}

	path := os.Args[0]

	var args []string
	for _, arg := range os.Args {
		if arg == "-graceful" {
			break
		}
		args = append(args, arg)
	}
	args = append(args, "-graceful")

	execSpec := &syscall.ProcAttr{
		Env:   os.Environ(),
		Files: []uintptr{os.Stdin.Fd(), os.Stdout.Fd(), os.Stderr.Fd(), listenerFd},
	}

	fork, err := syscall.ForkExec(path, args, execSpec)
	if err != nil {
		return fmt.Errorf("failed to forkexec: %v.", err)
	}

	// 通过设置超时使得老进程不再接受新请求
	this.listener.(*Listener).SetDeadline(time.Now())

	// 关闭老进程的链接
	this.listener.(*Listener).Close()

	this.logf("fork exec to pid %d.", fork)

	return nil
}

func (this *GraceHttp) logf(format string, args ...interface{}) {

	if this.Server.ErrorLog != nil {
		this.Server.ErrorLog.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}
