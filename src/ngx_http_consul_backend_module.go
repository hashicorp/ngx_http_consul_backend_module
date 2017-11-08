// package name: ngx_http_consul_backend_module
package main

import (
	"C"

	"context"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"
)

var (
	// client is the underlying API client.
	client *api.Client

	// resultNoBackend is the result returned when there is no backend.
	resultNoBackend = C.CString("")
)

const (
	// ctxTimeout is the default context timeout.
	ctxTimeout = 5 * time.Second

	// serviceTagSep is the separator between service names and tags.
	serviceTagSep = "."
)

// main is required for the file to compile to an object.
func main() {}

// setup the consul client
func init() {
	c, err := api.NewClient(api.DefaultConfig())
	if err != nil {
		log.Fatal(err)
	}
	client = c
}

//export LookupBackend
func LookupBackend(svc *C.char) *C.char {
	service, tag := extractService(C.GoString(svc))

	log.Printf("[debug] consul: lookup service=%s, tag=%s", service, tag)

	list, err := backends(service, tag)
	if err != nil {
		log.Fatal(err)
	}
	if len(list) < 1 {
		return resultNoBackend
	}

	i := rand.Intn(len(list))

	log.Printf("[debug] consul: returned %d services", len(list))

	return C.CString(list[i])
}

// extractService tags a string in the form "tag.name" and separates it into
// the service and tag name parts.
func extractService(s string) (service, tag string) {
	split := strings.SplitN(s, serviceTagSep, 2)

	switch {
	case len(split) == 0:
	case len(split) == 1:
		service = split[0]
	default:
		tag, service = split[0], split[1]
	}

	return
}

// backends collects the list of healthy backends for the given service name and tag,
// and returns
func backends(name, tag string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ctxTimeout)
	defer cancel()

	q := &api.QueryOptions{
		AllowStale: true,
	}
	q = q.WithContext(ctx)

	services, _, err := client.Health().Service(name, tag, true, q)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup service %q: %s", name, err)
	}

	addrs := make([]string, len(services))
	for i, s := range services {
		addr := s.Service.Address
		if addr == "" {
			addr = s.Node.Address
		}
		addrs[i] = fmt.Sprintf("%s:%d", addr, s.Service.Port)
	}

	sort.Strings(addrs)
	return addrs, nil
}
