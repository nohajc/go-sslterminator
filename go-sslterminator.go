package main

import (
	"context"
	"crypto/tls"
	"flag"
	"io"
	"log"
	"net"
	"runtime"
	"strings"

	//"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

var localAddress string
var backendAddress string
var certificatePath string
var keyPath string

func init() {
	flag.StringVar(&localAddress, "l", ":44300", "local address")
	flag.StringVar(&backendAddress, "b", ":8000", "backend address")
	flag.StringVar(&certificatePath, "c", "cert.pem", "SSL certificate path")
	flag.StringVar(&keyPath, "k", "key.pem", "SSL key path")
}

type DockerHelper struct {
	cli            *client.Client
	containerPort  string
	currentBackend string
}

func newDockerHelper(containerPort string) *DockerHelper {
	cli, err := client.NewEnvClient()
	if err != nil {
		panic(err)
	}

	return &DockerHelper{
		cli:           cli,
		containerPort: containerPort,
	}
}

func (dh *DockerHelper) getBackend(addr string) string {
	if strings.HasPrefix(addr, "dyn") {
		// get port mapping from docker container
		parts := strings.Split(addr, ":")
		if len(parts) < 2 {
			panic("invalid backend")
		}
		containerID := parts[1]
		resp, err := dh.cli.ContainerInspect(context.Background(), containerID)
		if err != nil {
			return dh.currentBackend
		}

		backend := "none"
		for k, v := range resp.NetworkSettings.Ports {
			if k.Port() == dh.containerPort {
				for _, binding := range v {
					if binding.HostIP == "0.0.0.0" {
						backend = ":" + binding.HostPort
						break
					}
				}
			}
		}
		if dh.currentBackend != backend {
			if dh.currentBackend != "" {
				log.Printf("backend changed to %s", backend)
			}
			dh.currentBackend = backend
		}
		return backend
	}
	return addr
}

func main() {
	flag.Parse()

	runtime.GOMAXPROCS(runtime.NumCPU())

	cert, err := tls.LoadX509KeyPair(certificatePath, keyPath)
	if err != nil {
		log.Fatalf("error in tls.LoadX509KeyPair: %s", err)
	}

	config := tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
		NextProtos:         []string{"h2"},
	}

	listener, err := tls.Listen("tcp", localAddress, &config)
	if err != nil {
		log.Fatalf("error in tls.Listen: %s", err)
	}

	log.Printf("local server on: %s, backend server on: %s", localAddress, backendAddress)

	helper := newDockerHelper(localAddress[1:])
	realPort := helper.getBackend(backendAddress)
	if backendAddress != realPort {
		log.Printf("current backend: %s", realPort)
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("error in listener.Accept: %s", err)
			break
		}

		go handle(conn, helper)
	}
}

func handle(clientConn net.Conn, helper *DockerHelper) {
	tlsconn, ok := clientConn.(*tls.Conn)
	if ok {

		err := tlsconn.Handshake()
		if err != nil {
			log.Printf("error in tls.Handshake: %s", err)
			clientConn.Close()
			return
		}

		backendConn, err := net.Dial("tcp", helper.getBackend(backendAddress))
		if err != nil {
			log.Printf("error in net.Dial: %s", err)
			clientConn.Close()
			return
		}

		go Tunnel(clientConn, backendConn)
		go Tunnel(backendConn, clientConn)
	}
}

func Tunnel(from, to io.ReadWriteCloser) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("recovered while tunneling")
		}
	}()

	io.Copy(from, to)
	to.Close()
	from.Close()
	log.Printf("tunneling is done")
}
