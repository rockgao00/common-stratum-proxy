package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type MinerConfig struct {
	Auth     string `json:"auth"`
	Pass     string `json:"pass"`
	Ipenable bool   `json:"ipenable"`
}

type Config struct {
	Listen     string      `json:"listen"`
	BTCTargets []string    `json:"btc_targets"`
	LTCTargets []string    `json:"ltc_targets"`
	Miner      MinerConfig `json:"miner"`
}

func getClientIP(conn net.Conn) string {
	clientIP := conn.RemoteAddr().(*net.TCPAddr).IP.String()
	formattedIP := strings.ReplaceAll(clientIP, ".", "x")
	return formattedIP
}

func ModifyJSON(data string, config *Config, ip string) string {
	var jsonData map[string]interface{}
	err := json.Unmarshal([]byte(data), &jsonData)
	if err != nil {
		log.Printf("Error unmarshalling JSON: %v", err)
		return data
	}

	if method, ok := jsonData["method"]; ok {
		switch method {
		case "mining.authorize":
			if params1, ok := jsonData["params"].([]interface{}); ok {
				if false == config.Miner.Ipenable {
					params1[0] = config.Miner.Auth
				} else {
					params1[0] = config.Miner.Auth + ip
				}
				jsonData["params"] = params1
			}
		case "mining.submit":
			if params2, ok := jsonData["params"].([]interface{}); ok {
				if false == config.Miner.Ipenable {
					params2[0] = config.Miner.Auth
				} else {
					params2[0] = config.Miner.Auth + ip
				}
				jsonData["params"] = params2
			}
		default:
		}

		modifiedData, err := json.Marshal(jsonData)
		if err != nil {
			log.Printf("Error marshalling JSON: %v", err)
			return data
		}
		return string(modifiedData)
	}

	return data
}

func checkPort(ip string, port int) bool {
	address := fmt.Sprintf("%s:%d", ip, port)
	timeout := time.Second * 2
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func HandleClient(clientConn net.Conn, config *Config, wg *sync.WaitGroup) {
	defer wg.Done()
	defer clientConn.Close()

	var targets []string
	if true == checkPort(clientConn.RemoteAddr().(*net.TCPAddr).IP.String(), 8359) {
		targets = config.LTCTargets
	} else if true == checkPort(clientConn.RemoteAddr().(*net.TCPAddr).IP.String(), 4028) {
		targets = config.BTCTargets
	} else {
		targets = config.LTCTargets
	}

	var remoteConn net.Conn
	var err error
	for index := 0; index < len(targets); index++ {
		remoteConn, err = net.Dial("tcp", targets[index])
		if err != nil {
			continue
		} else {
			break
		}
	}
	defer remoteConn.Close()
	if err != nil {
		log.Printf("Failed to connect to all remote server")
		return
	}

	clientReader := bufio.NewReader(clientConn)
	remoteReader := bufio.NewReader(remoteConn)

	var clientWg sync.WaitGroup
	clientWg.Add(2)

	go func() {
		defer clientWg.Done()
		for {
			clientData, err := clientReader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					log.Printf("Error reading from client: %v", err)
				}
				break
			}

			modifiedData := ModifyJSON(strings.TrimSpace(clientData), config, getClientIP(clientConn))
			_, err = remoteConn.Write([]byte(modifiedData + "\n"))
			if err != nil {
				log.Printf("Error writing to remote server: %v", err)
				break
			}
		}
	}()

	go func() {
		defer clientWg.Done()
		for {
			remoteData, err := remoteReader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					log.Printf("Error reading from remote server: %v", err)
				}
				break
			}
			_, err = clientConn.Write([]byte(remoteData))
			if err != nil {
				log.Printf("Error writing to client: %v", err)
				break
			}
		}
	}()

	clientWg.Wait()
}

func StartProxy(config *Config) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", config.Listen)
	if err != nil {
		log.Fatalf("Failed to resolve TCP address: %v", err)
	}

	listener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		log.Fatalf("Failed to start proxy server: %v", err)
	}
	defer listener.Close()

	log.Printf("Listening on %s", config.Listen)

	var wg sync.WaitGroup
	// Channel to receive OS signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	// Channel to notify the main goroutine to stop accepting new connections
	stopChan := make(chan struct{})

	go func() {
		for {
			select {
			case <-stopChan: // Stop accepting new connections
				return
			default:
				// Set a timeout on Accept to check the stopChan periodically
				//listener.SetDeadline(time.Now().Add(1 * time.Second))
				clientConn, err := listener.Accept()
				if err != nil {
					continue
				}

				//log.Printf("Accepted connection from %s", clientConn.RemoteAddr().String())
				wg.Add(1)
				go HandleClient(clientConn, config, &wg)
			}
		}
	}()

	<-sigChan
	close(stopChan)
	listener.Close()
	//wg.Wait()
	log.Println("Proxy server stopped")
}

func loadConfig(path string) (*Config, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	err = json.Unmarshal(file, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

func main() {
	configPath := flag.String("c", "config.json", "Path to JSON configuration file")
	logPath := flag.String("l", "", "Path to log configuration file")
	flag.Parse()

	logFile, err := os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer func(logFile *os.File) {
		err := logFile.Close()
		if err != nil {
			log.Printf("Error closing log file: %v", err)
		}
	}(logFile)

	log.SetOutput(logFile)

	config, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	if (len(config.BTCTargets) == 0 && len(config.LTCTargets) == 0) || len(config.Miner.Auth) == 0 {
		log.Fatal("No target addresses specified in config or auth is null")
	}

	log.Printf("Proxy server start")
	StartProxy(config)
}
