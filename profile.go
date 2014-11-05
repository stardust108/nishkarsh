// Package profile provides a simple way to manage runtime/pprof
// profiling of your Go application.
package profile

import (
	"flag"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync/atomic"
)

// memProfileRate holds the rate for the memory profile.
var memProfileRate = 4096

// started counts the number of times Start has been called
var started uint32

const (
	cpuMode = iota
	memMode
	blockMode
)

var (
	cpuFlag     = flag.Bool("cpuprofile", false, "Enables CPU profile.")
	memFlag     = flag.Bool("memprofile", false, "Enables memory profile.")
	blockFlag   = flag.Bool("blockprofile", false, "Enables goroutine blocking profile.")
	memRateFlag = flag.Int("memprofilerate", 0, "Enables memory profile at the given rate.")
	outputFlag  = flag.String("outputdir", "", "Sets the directory where the profile will be written.")
)

type profile struct {
	// quiet suppresses informational messages during profiling.
	quiet bool

	// noShutdownHook controls whether the profiling package should
	// hook SIGINT to write profiles cleanly.
	noShutdownHook bool

	// mode holds the type of profiling that will be made
	mode int

	// path holds the base path where various profiling files are  written.
	// If blank, the base path will be generated by ioutil.TempDir.
	path string

	// closers holds the cleanup functions that run after each profile
	closers []func()
}

// NoShutdownHook controls whether the profiling package should
// hook SIGINT to write profiles cleanly.
// Programs with more sophisticated signal handling should set
// this to true and ensure the Stop() function returned from Start()
// is called during shutdown.
func NoShutdownHook(p *profile) { p.noShutdownHook = true }

// Quiet suppresses informational messages during profiling.
func Quiet(p *profile) { p.quiet = true }

// CPUProfile controls if cpu profiling will be enabled. It disables any previous profiling settings.
func CPUProfile(p *profile) { p.mode = cpuMode }

// MemProfile controls if memory profiling will be enabled. It disables any previous profiling settings.
func MemProfile(p *profile) { p.mode = memMode }

// MemProfileRate controls if memory profiling will be enabled. Additionally, it takes a parameter which
// allows the setting of the memory profile rate.
func MemProfileRate(rate int) func(*profile) {
	return func(p *profile) {
		memProfileRate = rate
		p.mode = memMode
	}
}

// BlockProfile controls if block (contention) profiling will be enabled. It disables any previous profiling settings.
func BlockProfile(p *profile) { p.mode = blockMode }

// ProfilePath controls the base path where various profiling
// files are written. If blank, the base path will be generated
// by ioutil.TempDir.
func ProfilePath(path string) func(*profile) {
	return func(p *profile) {
		p.path = path
	}
}

// Stop stops the profile and flushes any unwritten data.
func (p *profile) Stop() {
	for _, c := range p.closers {
		c()
	}
}

// parseFlags analyzes the command line flags and applies them to the given profile.
func parseFlags(p *profile) {
	flag.Parse()

	switch true {
	case *cpuFlag:
		p.mode = cpuMode
	case *memFlag:
		p.mode = memMode
	case *blockFlag:
		p.mode = blockMode
	}

	if *memRateFlag != 0 {
		memProfileRate = *memRateFlag
		p.mode = memMode
	}
	if *outputFlag != "" {
		p.path = *outputFlag
	}
}

// Start starts a new profiling session.
// The caller should call the Stop method on the value returned
// to cleanly stop profiling.
func Start(options ...func(*profile)) interface {
	Stop()
} {
	if !atomic.CompareAndSwapUint32(&started, 0, 1) {
		log.Fatal("profile: Start() already called")
	}

	var prof profile
	for _, option := range options {
		option(&prof)
	}
	parseFlags(&prof)

	path, err := func() (string, error) {
		if p := prof.path; p != "" {
			return p, os.MkdirAll(p, 0777)
		}
		return ioutil.TempDir("", "profile")
	}()

	if err != nil {
		log.Fatalf("profile: could not create initial output directory: %v", err)
	}

	switch prof.mode {
	case cpuMode:
		fn := filepath.Join(path, "cpu.pprof")
		f, err := os.Create(fn)
		if err != nil {
			log.Fatalf("profile: could not create cpu profile %q: %v", fn, err)
		}
		if !prof.quiet {
			log.Printf("profile: cpu profiling enabled, %s", fn)
		}
		pprof.StartCPUProfile(f)
		prof.closers = append(prof.closers, func() {
			pprof.StopCPUProfile()
			f.Close()
		})

	case memMode:
		fn := filepath.Join(path, "mem.pprof")
		f, err := os.Create(fn)
		if err != nil {
			log.Fatalf("profile: could not create memory profile %q: %v", fn, err)
		}
		old := runtime.MemProfileRate
		runtime.MemProfileRate = memProfileRate
		if !prof.quiet {
			log.Printf("profile: memory profiling enabled (rate %d), %s", memProfileRate, fn)
		}
		prof.closers = append(prof.closers, func() {
			pprof.Lookup("heap").WriteTo(f, 0)
			f.Close()
			runtime.MemProfileRate = old
		})

	case blockMode:
		fn := filepath.Join(path, "block.pprof")
		f, err := os.Create(fn)
		if err != nil {
			log.Fatalf("profile: could not create block profile %q: %v", fn, err)
		}
		runtime.SetBlockProfileRate(1)
		if !prof.quiet {
			log.Printf("profile: block profiling enabled, %s", fn)
		}
		prof.closers = append(prof.closers, func() {
			pprof.Lookup("block").WriteTo(f, 0)
			f.Close()
			runtime.SetBlockProfileRate(0)
		})
	}

	if !prof.noShutdownHook {
		go func() {
			c := make(chan os.Signal, 1)
			signal.Notify(c, os.Interrupt)
			<-c

			log.Println("profile: caught interrupt, stopping profiles")
			prof.Stop()

			os.Exit(0)
		}()
	}

	prof.closers = append(prof.closers, func() {
		atomic.SwapUint32(&started, 0)
	})

	return &prof
}
