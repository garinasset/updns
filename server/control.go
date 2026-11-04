package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/garinasset/updns/logging"
	"github.com/miekg/dns"
)

const controlSocketPath = "/var/run/updns.sock"

type controlRequest struct {
	Domain string `json:"domain"`
	Ping   bool   `json:"ping,omitempty"`
}

type controlResponse struct {
	Results []*QueryResult `json:"results,omitempty"`
	Error   string         `json:"error,omitempty"`
	Ready   bool           `json:"ready,omitempty"`
}

func listenControlSocket(path string) (net.Listener, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket path")
		}

		conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
				return nil, fmt.Errorf("check existing control socket: %w", err)
			}
			if err := json.NewEncoder(conn).Encode(controlRequest{Ping: true}); err != nil {
				return nil, fmt.Errorf("existing control socket is active but did not respond: %w", err)
			}
			var response controlResponse
			if err := json.NewDecoder(conn).Decode(&response); err != nil || !response.Ready {
				if err != nil {
					return nil, fmt.Errorf("existing control socket is active but did not respond: %w", err)
				}
				return nil, fmt.Errorf("existing control socket is active but did not become ready")
			}
			return nil, fmt.Errorf("control socket is already active")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) && !os.IsNotExist(dialErr) {
			return nil, fmt.Errorf("check existing control socket: %w", dialErr)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale control socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect control socket path: %w", err)
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("secure control socket: %w", err)
	}
	return listener, nil
}

// QueryRunningService queries the running UpDNS instance over its local control socket.
func QueryRunningService(domain string) ([]*QueryResult, error) {
	return queryControlSocket(controlSocketPath, domain)
}

func queryControlSocket(path, domain string) ([]*QueryResult, error) {
	conn, err := net.DialTimeout("unix", path, 300*time.Millisecond)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return nil, fmt.Errorf("set query deadline: %w", err)
	}

	if err := json.NewEncoder(conn).Encode(controlRequest{Domain: domain}); err != nil {
		return nil, fmt.Errorf("send query to UpDNS service: %w", err)
	}

	var response controlResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return nil, fmt.Errorf("read query result from UpDNS service: %w", err)
	}
	if len(response.Results) == 0 && response.Error != "" {
		return nil, errors.New(response.Error)
	}
	return response.Results, nil
}

// ControlSocketUnavailable reports whether the service is not listening yet.
func ControlSocketUnavailable(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, syscall.ECONNREFUSED)
}

func (s *Server) serveControl() {
	for {
		conn, err := s.controlListener.Accept()
		if err != nil {
			select {
			case <-s.stopChan:
				return
			default:
			}
			logging.Errorf("control socket accept failed: %v", err)
			return
		}
		go s.handleControlConnection(conn)
	}
}

func (s *Server) handleControlConnection(conn net.Conn) {
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		logging.Warnf("failed to set CLI query deadline: %v", err)
		return
	}

	var request controlRequest
	if err := json.NewDecoder(conn).Decode(&request); err != nil {
		writeControlResponse(conn, controlResponse{Error: fmt.Sprintf("invalid control request: %v", err)})
		return
	}
	if request.Ping {
		writeControlResponse(conn, controlResponse{Ready: true})
		return
	}
	request.Domain = strings.TrimSpace(request.Domain)
	if request.Domain == "" || len(request.Domain) > 253 {
		writeControlResponse(conn, controlResponse{Error: "domain must contain 1 to 253 characters"})
		return
	}

	results, err := s.QueryDomain(request.Domain)
	if s.logQuery {
		for _, result := range results {
			logging.Infof("%s", FormatQueryLog("CLI Query", result, "", ""))
		}
	}
	response := controlResponse{Results: results}
	if err != nil {
		response.Error = err.Error()
	}
	writeControlResponse(conn, response)
}

func writeControlResponse(conn net.Conn, response controlResponse) {
	if err := json.NewEncoder(conn).Encode(response); err != nil {
		logging.Warnf("failed to write CLI query response: %v", err)
	}
}

// QueryDomain queries A and AAAA records independently and preserves per-type metadata.
func (s *Server) QueryDomain(domain string) ([]*QueryResult, error) {
	results := make([]*QueryResult, 0, 2)
	var queryErrors []error
	for _, queryType := range []uint16{dns.TypeA, dns.TypeAAAA} {
		result, err := s.Query(domain, queryType)
		if err != nil {
			failed := result
			if failed == nil {
				failed = s.queryMetadata(dns.Fqdn(domain), queryType)
			}
			failed.Rcode = dns.RcodeServerFailure
			failed.Error = failureReason(err)
			results = append(results, failed)
			queryErrors = append(queryErrors, fmt.Errorf("%s query failed: %w", dns.TypeToString[queryType], err))
			continue
		}
		results = append(results, result)
	}
	if len(queryErrors) == len(results) {
		return results, errors.Join(queryErrors...)
	}
	return results, nil
}
