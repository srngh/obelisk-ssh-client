package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

type SSHConfig struct {
	Username       string     `json:"username"`
	Address        string     `json:"address"`
	Password       string     `json:"password"`
	PrivateKeyPath string     `json:"private_key_path"`
	JumpHost       *SSHConfig `json:"jump_host"`
}

func sanitizeSize(width, height int) (int, int) {
	if width < 10 || height < 10 {
		return 80, 24
	}
	return width, height
}

func buildClientConfig(cfg *SSHConfig) (*ssh.ClientConfig, error) {
	var authMethods []ssh.AuthMethod

	// Prioritize Private Key if provided
	if cfg.PrivateKeyPath != "" {
		key, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("unable to read private key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("unable to parse private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	} else if cfg.Password != "" {
		// Fallback to Password Auth
		authMethods = append(authMethods, ssh.Password(cfg.Password))
	}

	return &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}, nil
}

func dialRecursive(cfg *SSHConfig) (*ssh.Client, error) {
	sshCfg, err := buildClientConfig(cfg)
	if err != nil {
		return nil, err
	}

	if cfg.JumpHost == nil {
		return ssh.Dial("tcp", cfg.Address, sshCfg)
	}

	jumpClient, err := dialRecursive(cfg.JumpHost)
	if err != nil {
		return nil, fmt.Errorf("Failed to connect to jump host: %w", err)
	}

	netConn, err := jumpClient.Dial("tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("failed to route through jump host: %w", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, cfg.Address, sshCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to establish ssh handshake over jump: %w", err)
	}

	return ssh.NewClient(sshConn, chans, reqs), nil

}

func loadConfig(filePath string) (*SSHConfig, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	var config SSHConfig
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to decode json: %w", err)
	}

	return &config, nil
}

func main() {
	//fmt.Println("Launching custom client")

	configPath := os.Getenv("SSH_CONFIG_PATH")
	if configPath == "" {
		log.Fatal("SSH_CONFIG_PATH environment variable not set")
	}
	// fmt.Printf("Using tmp file: %v\n", configPath)

	os.Unsetenv("SSH_CONFIG_PATH")

	defer os.Remove(configPath)

	targetConfig, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// fmt.Printf("Using Config %v\n", &targetConfig)

	client, err := dialRecursive(targetConfig)
	if err != nil {
		log.Fatalf("SSH Connection failed: %v", err)
	}

	defer client.Close()

	// Put the local terminal in raw mode
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		log.Fatal("Failed to put Terminal in raw mode", err)
	}

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
				// Start a 2ms countdown
				timer = time.NewTimer(2 * time.Millisecond)

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
