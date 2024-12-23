package main

// Code provided kindly by Cbro
// https://www.twitch.tv/Cbro

import (
	"context"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"time"
  "fmt"

	"github.com/joho/godotenv"
	"github.com/paulbellamy/ratecounter"
	"golang.org/x/time/rate"
)

var (
  ipv6interface = flag.String("Interface", "enp1s0", "ipv6 interface")
  ipv6n = flag.Int("v6_n", 1, "Number of sequential ipv6 addresses")
  port = flag.Int("port", 8082, "Port to listen on")
  printAddrs = flag.Bool("print_addrs", false, "print ipv6 addresses")
  verbose = flag.Bool("verbose", false, "print logs")
)

var (
  securityKey = ""
  rateIntervalSeconds = 10
  rateInterval = time.Second * time.Duration(rateIntervalSeconds)
  writeCounter = ratecounter.NewRateCounter(rateInterval)
  readCounter = ratecounter.NewRateCounter(rateInterval)
)

type transport struct {
	nW      int64
	nS      int64
	rt      []http.RoundTripper
	statsRl []*rate.Limiter
	wwwRl   []*rate.Limiter
}

var proxyTransport = &transport{}

func main() {
  flag.Parse()
  if err := godotenv.Load(); err != nil {
    log.Fatal("Error loading .env file")
  }

  securityKey := os.Getenv("BUNGIE_API_KEY")
  if securityKey == "" {
    log.Fatal("Failed to load Bungie API key from .env file")
  }
  
  addr := netip.MustParseAddr(os.Getenv("IPV6"))
  for i := 0; i < *ipv6n; i++ {
      d := &net.Dialer{
          LocalAddr: &net.TCPAddr{
            IP: net.IP(addr.AsSlice()),
          },
          Timeout: 30 * time.Second,
          KeepAlive: 30 * time.Second,
      }
      rt := http.DefaultTransport.(*http.Transport).Clone()
      rt.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
        conn, err := d.DialContext(ctx, network, addr)
        if err != nil {
          log.Fatal("Something wrong happened with the dial context")
          return conn, err 
        }
        return conn, err
      }
      proxyTransport.statsRl = append(proxyTransport.statsRl, rate.NewLimiter(rate.Every(time.Second/30), 90)) 
      proxyTransport.wwwRl = append(proxyTransport.wwwRl, rate.NewLimiter(rate.Every(time.Second/12), 90))
      proxyTransport.rt = append(proxyTransport.rt, rt)
  }
  rp := &httputil.ReverseProxy{
    Director: func(r *http.Request) {
      if strings.Contains(r.URL.Path, "Destiny2/Stats/PostGameCarnageReport") {
        r.URL.Host = "stats.bungie.net"
      } else {
        r.URL.Host = "www.bungie.net"
      }
      r.URL.Scheme = "https"
      r.Header.Set("User-Agent", "")
      r.Header.Del("x-forwarded-for")
    },
    Transport: proxyTransport,
  }

  mainHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    if r.Header.Get("x-betteruptime-probe") != "" {
      io.WriteString(w, "ok")
      return
    }
    rp.ServeHTTP(w, r)
  })

  log.Printf("Reverse proxy ready on port %d\n", *port)

  // Start the web server
  log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), mainHandler))
}

func (t* transport) RoundTrip(r *http.Request) (*http.Response, error) {
  var rl *rate.Limiter
  var n int64
  if (strings.Contains(r.URL.Path, "Destiny2/Stats/PostGameCarnageReport")) {
    n = atomic.AddInt64(&t.nS, 1) 
    r.Host = "stats.bungie.net"
    rl = t.statsRl[n % int64(len(t.statsRl))]
  } else {
    n = atomic.AddInt64(&t.nW, 1)
    r.Host = "www.bungie.net"
    rl= t.wwwRl[n % int64(len(t.wwwRl))]
  }
  if r.Header.Get("x-api-key") == securityKey {
    if *verbose {
      log.Printf("Security key provided: %s\n", r.Header.Get("x-api-key"))
      log.Printf("Using API key: %s\n", securityKey) 
    }
    r.Header.Set("x-api-key", securityKey)
    r.Header.Add("x-forwarded-for", securityKey)
  }

  if(*verbose) {
    log.Printf("Sending request: %s\n", r.URL.String())
    log.Printf("Request Headers: %s\n", r.Header)
  }

  rt := t.rt[n % int64(len(t.rt))]
  rl.Wait(r.Context())
  return rt.RoundTrip(r)
} 
