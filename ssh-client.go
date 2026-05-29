package main

import (
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

type SSHConfig struct {
	Username       string   `json:"username"`
	Address        string   `json:"address"`
	Port           string   `json:"port"`
	Password       string   `json:"password"`
	PrivateKeyPath string   `json:"private_key_path"`
	JumpHosts      []string `json:"jump_hosts"`
}

func sanitizeSize(width, height int) (int, int) {
	if width < 10 || height < 10 {
		return 80, 24
	}
	return width, height
}

func main() {
	//fmt.Println("Launching custom client")

	configPath := os.Getenv("SSH_CONFIG_PATH")
	if configPath == "" {
		log.Fatal("SSH_CONFIG_PATH environment variable not set")
	}

	// 2. Clean up the environment variable
	os.Unsetenv("SSH_CONFIG_PATH")

	// 3. Defer the deletion of the file so it is destroyed immediately
	// after we are done reading, ensuring no credentials linger.
	defer os.Remove(configPath)

	// 4. Open and decode the file
	file, err := os.Open(configPath)
	if err != nil {
		log.Fatalf("Failed to open config file: %v", err)
	}
	defer file.Close()

	var config SSHConfig
	if err := json.NewDecoder(file).Decode(&config); err != nil {
		log.Fatalf("Failed to decode configuration: %v", err)
	}

	//fdStr := os.Getenv("SSH_CONFIG_FD")
	//if fdStr == "" {
	//	log.Fatal("SSH_CONFIG_FD environment variable not set")
	//}
	//// Clean up the environment variable immediately so child processes don't see it
	//os.Unsetenv("SSH_CONFIG_FD")

	//fdNum, err := strconv.Atoi(fdStr)
	//if err != nil {
	//	log.Fatalf("Invalid File Descriptor: %v", err)
	//}

	//fmt.Printf("File Descriptor: %d", fdNum)

	//// 2. Wrap the raw file descriptor in an os.File
	//// The name "config_pipe" is arbitrary and just used for internal Go logging
	//pipeFile := os.NewFile(uintptr(fdNum), "config_pipe")
	//if pipeFile == nil {
	//	log.Fatalf("Failed to open pipe file descriptor %d", fdNum)
	//}

	//// 3. Read and decode the JSON payload
	//var config SSHConfig
	//if err := json.NewDecoder(pipeFile).Decode(&config); err != nil {
	//	pipeFile.Close()
	//	log.Fatalf("Failed to decode configuration: %v", err)
	//}

	//// 4. Close the pipe now that we have the data in memory
	//pipeFile.Close()

	//// 5. Proceed with your SSH connection using `config`!

	//var hostkey ssh.PublicKey

	//fmt.Printf("Successfully loaded config for %s@%s\n", config.Username, config.Address)
	ssh_config := &ssh.ClientConfig{
		User: config.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(config.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		//HostKeyCallback: ssh.FixedHostKey(hostkey),
	}

	a_p := config.Address + ":" + config.Port
	client, err := ssh.Dial("tcp", a_p, ssh_config)
	if err != nil {
		log.Fatal("Failed to dial: ", err)
	}

	defer client.Close()

	// Put the local terminal in raw mode
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		log.Fatal("Failed to put Terminal in raw mode", err)
	}

	// Ensure we restore the terminal to its normal state on exit
	defer term.Restore(fd, oldState)

	session, err := client.NewSession()
	if err != nil {
		log.Fatal("Failed to create session: ", err)
	}
	defer session.Close()

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr
	session.Stdin = os.Stdin

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}

	termType := os.Getenv("TERM")
	if termType == "" {
		termType = "xterm-256color" // Fallback to color-supported terminal
	}

	termWidth, termHeight, err := term.GetSize(fd)
	if err != nil {
		log.Println("Could not get Term size ", err)
		termWidth, termHeight = 80, 24 // Safe fallbacks
	}
	// fmt.Printf("Term Width: %d, Term Height: %d", termWidth, termHeight)

	termWidth, termHeight = sanitizeSize(termWidth, termHeight)

	if err := session.RequestPty("xterm-256color", termWidth, termHeight, modes); err != nil {
		log.Fatalf("request for pseudo terminal failed: %s", err)
	}

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch) // Clean up the listener when the session ends

	// Launch a goroutine to handle the resize events in the background
	//go func() {
	//	for range winch {
	//		// Get the new terminal dimensions
	//		newWidth, newHeight, err := term.GetSize(fd)
	//		if err == nil {
	//			// Send the updated dimensions to the remote server
	//			session.WindowChange(newHeight, newWidth)
	//		}
	//	}
	//}()

	go func() {
		var timer *time.Timer
		var timeout <-chan time.Time
		var lastWidth, lastHeight int

		for {
			select {
			case <-winch:
				if timer != nil {
					timer.Stop()
				}
				// Start a 20ms countdown
				timer = time.NewTimer(20 * time.Millisecond)

				timeout = timer.C

			case <-timeout:
				newWidth, newHeight, err := term.GetSize(int(os.Stdout.Fd()))
				if err == nil {
					newWidth, newHeight = sanitizeSize(newWidth, newHeight)
					if newWidth != lastWidth || newHeight != lastHeight {
						session.WindowChange(newHeight, newWidth)
						lastWidth = newWidth
						lastHeight = newHeight
					}
				}
				timeout = nil
			}
		}
	}()

	if err := session.Shell(); err != nil {
		log.Fatalf("failed to start shell: %s", err)
	}

	// force a Window Resize
	winch <- syscall.SIGWINCH

	if err := session.Wait(); err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			log.Printf("Remote Process exited with code: %d", exitErr.ExitStatus())
		} else {
			log.Printf("Local SSH session error: %v", err)
		}
	}
}
