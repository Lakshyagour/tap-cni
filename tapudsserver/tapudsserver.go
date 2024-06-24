package main

import (
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"syscall"
	"unsafe"
)

func createTAPDevice(name string) (*os.File, error) {
	file, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open /dev/net/tun: %w", err)
	}

	var req ifReq
	copy(req.Name[:], name)
	req.Flags = unix.IFF_TAP | unix.IFF_NO_PI

	if err := ioctl(file.Fd(), unix.TUNSETIFF, uintptr(unsafe.Pointer(&req))); err != nil {
		return nil, fmt.Errorf("failed to create TAP device: %w", err)
	}

	return file, nil
}

type ifReq struct {
	Name  [unix.IFNAMSIZ]byte
	Flags uint16
	_     [24 - unix.IFNAMSIZ - 2]byte // Padding
}

func ioctl(fd, req, arg uintptr) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, arg)
	if errno != 0 {
		return errno
	}
	return nil
}

func sendFD(conn *net.UnixConn, file *os.File) error {
	rights := syscall.UnixRights(int(file.Fd()))
	if _, _, err := conn.WriteMsgUnix(nil, rights, nil); err != nil {
		return fmt.Errorf("failed to send file descriptor: %w", err)
	}
	return nil
}

func main() {
	// Check if the correct number of arguments is provided
	if len(os.Args) != 2 {
		fmt.Println("Usage: go run main.go <devname>")
		return
	}

	// Get the device name from command-line arguments
	devname := os.Args[1]

	// Create the TAP device
	tapFile, err := createTAPDevice(devname)
	if err != nil {
		fmt.Println("Error creating TAP device:", err)
		return
	}
	defer tapFile.Close()

	// Set up Unix Domain Socket
	addr, err := net.ResolveUnixAddr("unix", "/tmp/tap_sock/"+devname+".sock")
	if err != nil {
		fmt.Println("Error resolving Unix address:", err)
		return
	}

	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		fmt.Println("Error setting up Unix Domain Socket listener:", err)
		return
	}
	defer listener.Close()

	fmt.Println("Server is listening...")

	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			fmt.Println("Error accepting connection:", err)
		}

		fmt.Println("Client connected, sending file descriptor...")
		if err := sendFD(conn, tapFile); err != nil {
			fmt.Println("Error sending file descriptor:", err)
		} else {
			fmt.Println("File descriptor sent successfully.")
		}
		conn.Close()
	}
}
