package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"gopkg.in/yaml.v3"

	"github.com/Devaansh-Kumar/Heimdall/pkg/cgroup"
	"github.com/Devaansh-Kumar/Heimdall/pkg/fileaccess"
	"github.com/Devaansh-Kumar/Heimdall/pkg/privilege"
	"github.com/Devaansh-Kumar/Heimdall/pkg/syscallfilter"
	"github.com/Devaansh-Kumar/Heimdall/pkg/x64"
	"github.com/cilium/ebpf/rlimit"
	"github.com/spf13/cobra"
)

type Config struct {
	ContainerID         string   `yaml:"container_id"`
	BlockSyscalls       []string `yaml:"block_syscalls"`
	BlockPrivilegeEscal bool     `yaml:"block_privilege_escalation"`
	BlockFilePaths      []string `yaml:"file_paths"`
}

// rootCmd represents the base command
var rootCmd = &cobra.Command{
	Use:   "heimdall",
	Short: "A CLI to add security to containers using eBPF",
	Long:  `A CLI to restrict file system access, block system calls and prevent privilege escalation via eBPF for containers.`,
	Run: func(cmd *cobra.Command, args []string) {
		flagContainerID, _ := cmd.Flags().GetString("container-id")
		flagSyscalls, _ := cmd.Flags().GetStringSlice("block-syscalls")
		flagPriv, _ := cmd.Flags().GetBool("block-privilege-escalation")
		flagPaths, _ := cmd.Flags().GetStringSlice("file-path")
		yamlPath, _ := cmd.Flags().GetString("yaml")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		// Remove resource limits for kernels <5.11.
		if err := rlimit.RemoveMemlock(); err != nil {
			log.Fatal("Removing memlock:", err)
		}

		var cfg Config
		if yamlPath != "" {
			data, err := os.ReadFile(yamlPath)
			if err != nil {
				log.Fatalf("Failed to read YAML file %s: %v", yamlPath, err)
			}
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				log.Fatalf("Failed to parse YAML: %v", err)
			}
		}

		containerID := cfg.ContainerID
		if containerID == "" {
			containerID = flagContainerID
		}
		syscalls := cfg.BlockSyscalls
		if len(syscalls) == 0 {
			syscalls = flagSyscalls
		}
		privEscalation := cfg.BlockPrivilegeEscal
		if !privEscalation {
			privEscalation = flagPriv
		}
		filePaths := cfg.BlockFilePaths
		if len(filePaths) == 0 {
			filePaths = flagPaths
		}

		// Get cgroup id from container name
		cgroupID, err := cgroup.GetCgroupID(containerID)
		if err != nil {
			log.Fatalf("Failed to get cgroup ID for container %s: %v", containerID, err)
		}

		var systemCallList []uint32
		for _, syscallName := range syscalls {
			// Convert syscall name to number
			syscallNum, err := x64.GetSyscallNumber(syscallName)
			if err != nil {
				log.Fatalf("Failed to get syscall number: %s", err)
			}
			systemCallList = append(systemCallList, uint32(syscallNum))
		}

		if dryRun {
			dryRunCmd(containerID, systemCallList, cgroupID, privEscalation, filePaths)
			return
		}

		// For synchronizing program loading and unloading on exit
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup

		// Set up signal handling to cancel context on Ctrl+C or SIGTERM
		go func() {
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			<-sig
			fmt.Println("Received signal, exiting...")
			cancel()
		}()

		started := false

		// Loading syscall blocker only if user provided syscalls
		// and adding to waitgroup
		if len(syscalls) > 0 {
			wg.Add(1)
			go syscallfilter.BlockSystemCall(ctx, &wg, systemCallList, cgroupID)
			started = true
		}

		// Loading privilege escalation blocker if requested
		// and adding to waitgroup
		if privEscalation {
			wg.Add(1)
			go privilege.BlockPrivilegeEscalation(ctx, &wg, cgroupID)
			started = true
		}

		if len(filePaths) > 0 {
			wg.Add(1)
			// fmt.Println("Blocking file access for paths:", filePath)
			go fileaccess.BlockFileOpen(ctx, &wg, cgroupID, filePaths)
			started = true
		}

		if started {
			<-ctx.Done() // Wait until termination signal is received
			wg.Wait()    // Wait for background tasks to finish
		} else {
			log.Println("No filters applied. Exiting")
		}

	},
}

// Execute runs the root command
func Execute() {
	rootCmd.Flags().StringP("container-id", "c", "", "Long Container ID")
	rootCmd.Flags().StringSliceP("block-syscalls", "s", []string{}, "List of system calls to block")
	rootCmd.Flags().BoolP("block-privilege-escalation", "p", false, "Block Privilege Escalation attempts for the container")
	rootCmd.Flags().StringSliceP("file-path", "f", []string{}, "File path to block")
	rootCmd.Flags().StringP("yaml", "y", "", "Path to YAML config file with container_id, block_syscalls, block_privilege_escalation, file_paths")
	rootCmd.Flags().Bool("dry-run", false, "Enable dry-run mode to preview actions without applying filters")

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func dryRunCmd(containerID string, syscalls []uint32, cgroupID uint64, privEscalation bool, filePaths []string) {
	fmt.Printf("Dry run mode enabled. The following actions would be taken:\n\n")

	if len(syscalls) > 0 {
		for _, syscall := range syscalls {
			syscallName, err := x64.GetSyscallName(int(syscall))
			if err != nil {
				fmt.Printf("Failed to get syscall name for number %d: %v\n", syscall, err)
				continue
			}
			fmt.Printf("Block system call: %s (Number: %d)\n", syscallName, syscall)
		}
	}

	if privEscalation {
		fmt.Println("Block privilege escalation attempts")
	}

	if len(filePaths) > 0 {
		fmt.Printf("Block file access for paths: %v\n", filePaths)
	}

	fmt.Println("\nNote: No changes will be made to the system.")
	fmt.Printf("Container ID: %s\n", containerID)
	fmt.Printf("Cgroup ID: %d\n", cgroupID)
}
