package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/webportdev/webport/internal/dnsmanager"

	cloudflare "github.com/libdns/cloudflare"
	digitalocean "github.com/libdns/digitalocean"
	route53 "github.com/libdns/route53"
)

type options struct {
	configPath      string
	credentialsPath string
	provider        string
	baseDomain      string
	zone            string
	ipv4            string
	ipv6            string
	ttl             time.Duration
}

func main() {
	if len(os.Args) < 2 || (os.Args[1] != "status" && os.Args[1] != "sync") {
		usage()
		os.Exit(2)
	}

	command := os.Args[1]
	opts, err := parseOptions(command, os.Args[2:])
	if err != nil {
		fatal(err)
	}
	if err := loadEnvFile(opts.configPath, false); err != nil {
		fatal(err)
	}
	applyEnvironment(&opts)
	if opts.provider == "" {
		fatal(fmt.Errorf("DNS provider is not configured"))
	}
	if opts.baseDomain == "" {
		fatal(fmt.Errorf("base domain is not configured"))
	}
	if opts.credentialsPath == "" {
		fatal(fmt.Errorf("DNS credentials file is not configured; use --credentials"))
	}
	if err := loadEnvFile(opts.credentialsPath, true); err != nil {
		fatal(err)
	}

	provider, err := newProvider(opts.provider)
	if err != nil {
		fatal(err)
	}
	manager := dnsmanager.New(provider)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var status dnsmanager.Status
	if command == "sync" {
		status, err = manager.Sync(ctx, opts.baseDomain, opts.zone, opts.ipv4, opts.ipv6, opts.ttl)
	} else {
		status, err = manager.Status(ctx, opts.baseDomain, opts.zone)
	}
	if err != nil {
		fatal(err)
	}
	printStatus(opts.baseDomain, status)
}

func parseOptions(command string, args []string) (options, error) {
	opts := options{configPath: defaultConfigPath(), ttl: dnsmanager.DefaultTTL}
	flags := flag.NewFlagSet("webport-dns "+command, flag.ContinueOnError)
	flags.StringVar(&opts.configPath, "config", opts.configPath, "webport environment file")
	flags.StringVar(&opts.credentialsPath, "credentials", "", "DNS provider credentials environment file")
	flags.StringVar(&opts.provider, "provider", "", "DNS provider override")
	flags.StringVar(&opts.baseDomain, "base-domain", "", "base domain override")
	flags.StringVar(&opts.zone, "zone", "", "authoritative DNS zone override")
	flags.DurationVar(&opts.ttl, "ttl", opts.ttl, "DNS record TTL")
	if command == "sync" {
		flags.StringVar(&opts.ipv4, "ipv4", "", "wildcard IPv4 address")
		flags.StringVar(&opts.ipv6, "ipv6", "", "optional wildcard IPv6 address")
	}
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	return opts, nil
}

func applyEnvironment(opts *options) {
	if opts.credentialsPath == "" {
		opts.credentialsPath = os.Getenv("WEBPORT_DNS_CREDENTIALS_FILE")
	}
	if opts.provider == "" {
		opts.provider = os.Getenv("WEBPORT_TLS_DNS_PROVIDER")
	}
	if opts.baseDomain == "" {
		opts.baseDomain = os.Getenv("WEBPORT_BASE_DOMAIN")
	}
	if opts.zone == "" {
		opts.zone = os.Getenv("WEBPORT_DNS_ZONE")
	}
}

func newProvider(name string) (dnsmanager.Provider, error) {
	switch name {
	case "cloudflare":
		token := os.Getenv("CLOUDFLARE_API_TOKEN")
		if token == "" {
			return nil, fmt.Errorf("CLOUDFLARE_API_TOKEN is required")
		}
		return &cloudflare.Provider{APIToken: token}, nil
	case "digitalocean":
		token := os.Getenv("DO_AUTH_TOKEN")
		if token == "" {
			return nil, fmt.Errorf("DO_AUTH_TOKEN is required")
		}
		return &digitalocean.Provider{APIToken: token}, nil
	case "route53":
		return &route53.Provider{
			Region:          os.Getenv("AWS_REGION"),
			Profile:         os.Getenv("AWS_PROFILE"),
			AccessKeyId:     os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
		}, nil
	case "":
		return nil, fmt.Errorf("DNS provider is not configured")
	default:
		return nil, fmt.Errorf("DNS record management does not support provider %q", name)
	}
}

func loadEnvFile(path string, required bool) error {
	file, err := os.Open(path)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open environment file %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return fmt.Errorf("invalid environment line in %s", path)
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func printStatus(baseDomain string, status dnsmanager.Status) {
	fmt.Printf("Zone: %s\n", strings.TrimSuffix(status.Zone, "."))
	fmt.Printf("Wildcard: *.%s\n", strings.Trim(baseDomain, "."))
	if len(status.Records) == 0 {
		fmt.Println("Records: none")
		return
	}
	for _, record := range status.Records {
		rr := record.RR()
		fmt.Printf("%s %s TTL=%s\n", rr.Type, rr.Data, rr.TTL)
	}
}

func defaultConfigPath() string {
	if runtime.GOOS == "darwin" {
		return "/usr/local/etc/webport/webport.env"
	}
	return "/etc/webport/webport.env"
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  webport-dns status [--zone ZONE]")
	fmt.Fprintln(os.Stderr, "  webport-dns sync --ipv4 ADDRESS [--ipv6 ADDRESS] [--zone ZONE]")
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
