package modd

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"

	"github.com/cortesi/modd/conf"
	"github.com/cortesi/termlog"
)

// MinRestart is the minimum amount of time between daemon restarts
const MinRestart = 1 * time.Second

const lineLimit = 80
const postamble = "..."

// niceHeader tries to produce a nicer process name. We condense whitespace to
// make commands split over multiple lines with indentation more legible, and
// limit the line length to 80 characters.
func niceHeader(preamble string, in string) string {
	pre := termlog.DefaultPalette.Timestamp.SprintFunc()(preamble)
	post := ""
	in = ws.ReplaceAllString(in, " ")
	if len(in) > lineLimit-len(postamble) {
		post = termlog.DefaultPalette.Say.SprintFunc()(postamble)
		in = in[:lineLimit-len(postamble)]
	}
	in = termlog.DefaultPalette.Header.SprintFunc()(in)
	return pre + in + post
}

func getShell() string {
	return "/bin/sh"
}

func logOutput(fp io.ReadCloser, out func(string, ...interface{})) {
	r := bufio.NewReader(fp)
	for {
		line, _, err := r.ReadLine()
		if err != nil {
			return
		}
		out(string(line))
	}
}

// RunProc runs a process to completion, sending output to log
func RunProc(cmd string, log termlog.Stream) error {
	log.Header()
	sh := getShell()
	c := exec.Command(sh, "-c", cmd)
	stdo, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	stde, err := c.StderrPipe()
	if err != nil {
		return err
	}
	go logOutput(stde, log.Warn)
	go logOutput(stdo, log.Say)
	err = c.Start()
	if err != nil {
		return err
	}
	err = c.Wait()
	if err != nil {
		log.Shout("%s", c.ProcessState.String())
		return err
	}
	// FIXME: rusage stats here
	log.NoticeAs("cmdstats", "run time: %s", c.ProcessState.UserTime())
	return nil
}

// RunPreps runs all commands in sequence. Stops if any command returns an error.
func RunPreps(preps []conf.Prep, log termlog.TermLog) error {
	for _, p := range preps {
		err := RunProc(
			p.Command,
			log.Stream(niceHeader("prep: ", p.Command)),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

type daemon struct {
	conf conf.Daemon
	log  termlog.Stream
	cmd  *exec.Cmd
	stop bool
}

func (d *daemon) Run() {
	var lastStart time.Time
	for d.stop != true {
		d.log.Header()
		since := time.Now().Sub(lastStart)
		if since < MinRestart {
			time.Sleep(MinRestart - since)
		}
		lastStart = time.Now()
		sh := getShell()
		c := exec.Command(sh, "-c", d.conf.Command)
		stdo, err := c.StdoutPipe()
		if err != nil {
			d.log.Shout("%s", err)
			continue
		}
		stde, err := c.StderrPipe()
		if err != nil {
			d.log.Shout("%s", err)
			continue
		}
		go logOutput(stde, d.log.Warn)
		go logOutput(stdo, d.log.Say)
		err = c.Start()
		if err != nil {
			d.log.Shout("%s", err)
			continue
		}
		d.cmd = c
		err = c.Wait()
		if err != nil {
			d.log.Shout("%s", c.ProcessState.String())
			continue
		}
	}
}

func (d *daemon) Restart() {
	if d.cmd != nil {
		d.log.Header()
		d.cmd.Process.Signal(d.conf.RestartSignal)
	}
}

func (d *daemon) Shutdown(sig os.Signal) {
	d.stop = true
	if d.cmd != nil {
		d.cmd.Process.Signal(sig)
		d.cmd.Wait()
	}
}

// DaemonPen is a group of daemons, managed as a unit.
type DaemonPen struct {
	daemons *[]daemon
	sync.Mutex
}

var ws = regexp.MustCompile(`\s\s+`)

// Start starts set of daemons, each specified by a command
func (dp *DaemonPen) Start(daemons []conf.Daemon, log termlog.TermLog) {
	dp.Lock()
	defer dp.Unlock()
	d := make([]daemon, len(daemons))
	for i, dmn := range daemons {
		d[i] = daemon{
			conf: dmn,
			log: log.Stream(
				niceHeader("daemon: ", dmn.Command),
			),
		}
		go d[i].Run()
	}
	dp.daemons = &d
}

// Restart all daemons in the pen
func (dp *DaemonPen) Restart() {
	dp.Lock()
	defer dp.Unlock()
	if dp.daemons != nil {
		for _, d := range *dp.daemons {
			d.Restart()
		}
	}
}

// Shutdown all daemons in the pen
func (dp *DaemonPen) Shutdown(sig os.Signal) {
	dp.Lock()
	defer dp.Unlock()
	if dp.daemons != nil {
		for _, d := range *dp.daemons {
			d.Shutdown(sig)
		}
	}
}
