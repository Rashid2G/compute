package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const bashCompletionScript = `# bash completion for compute
_compute_completion() {
    local cur prev words cword
    if declare -F _init_completion >/dev/null 2>&1; then
        _init_completion || return
    else
        cur="${COMP_WORDS[COMP_CWORD]}"
        prev="${COMP_WORDS[COMP_CWORD-1]}"
        words=("${COMP_WORDS[@]}")
        cword=$COMP_CWORD
    fi
    local completion_command="${words[0]}"

    local commands="sandbox vm ssh job jobs worker workers pool pools image images template templates gui completion help"
    local sandbox_subcommands="list create start stop delete exec ssh info"
    local job_subcommands="run list info logs cancel"
    local worker_subcommands="token list info cordon drain delete"
    local pool_subcommands="list create delete"
    local image_subcommands="list build inspect delete"
    local template_subcommands="list create launch inspect delete"
    local completion_subcommands="bash zsh fish install"

    if [[ $cword -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "$commands" -- "$cur") )
        return 0
    fi

    local cmd="${words[1]}"
    case "$cmd" in
        ssh)
            local vms=$("$completion_command" __complete vms 2>/dev/null)
            COMPREPLY=( $(compgen -W "$vms --user -u --password -p -t -T --help" -- "$cur") )
            return 0
            ;;
        sandbox|vm)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$sandbox_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -ge 3 ]]; then
                case "${words[2]}" in
                    create) COMPREPLY=( $(compgen -W "--name --tier --vcpus --memory --disk --image --template --ssh-key --help" -- "$cur") ) ;;
                    ssh)
                        local vms=$("$completion_command" __complete vms 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$vms --user -u --password -p -t -T --help" -- "$cur") )
                        return 0
                        ;;
                    start|run|stop|delete|rm|info|inspect|show|exec) COMPREPLY=( $(compgen -W "--help" -- "$cur") ) ;;
                esac
                return 0
            fi
            ;;
        job|jobs)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$job_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -ge 3 ]]; then
                case "${words[2]}" in
                    run) COMPREPLY=( $(compgen -W "--image --pool --vcpus --mem --files --cwd --follow --help" -- "$cur") ) ;;
                    info|logs|cancel|list|ls) COMPREPLY=( $(compgen -W "--help" -- "$cur") ) ;;
                esac
                return 0
            fi
            ;;
        worker)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$worker_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -ge 3 ]]; then
                COMPREPLY=( $(compgen -W "--pool --help" -- "$cur") )
                return 0
            fi
            ;;
        pool)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$pool_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -ge 3 ]]; then
                COMPREPLY=( $(compgen -W "--help" -- "$cur") )
                return 0
            fi
            ;;
        image)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$image_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -ge 3 ]]; then
                COMPREPLY=( $(compgen -W "--name --packages --base --size --desc --help" -- "$cur") )
                return 0
            fi
            ;;
        template)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$template_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -ge 3 ]]; then
                COMPREPLY=( $(compgen -W "--vm --desc --name --tier --help" -- "$cur") )
                return 0
            fi
            ;;
        completion|autocomplete)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$completion_subcommands" -- "$cur") )
                return 0
            fi
            ;;
    esac
}
complete -F _compute_completion compute
`

const zshCompletionScript = `#compdef compute

_compute() {
    local -a commands
    commands=(
        'sandbox:Manage isolated compute sandboxes'
        'vm:Manage isolated compute sandboxes (alias for sandbox)'
        'job:Run and monitor distributed compute jobs'
        'worker:Manage worker nodes and enrollments'
        'pool:Manage worker pools'
        'image:Manage base and custom rootfs images'
        'template:Manage VM disk templates'
        'gui:Launch Web UI dashboard'
        'completion:Generate shell autocompletion script'
        'help:Show help documentation'
    )

    if (( CURRENT == 2 )); then
        _describe 'command' commands
        return
    fi
    local -a subcommands
    case "${words[2]}" in
        sandbox|vm) subcommands=('list:List sandboxes' 'create:Create a sandbox' 'start:Start sandboxes' 'stop:Stop sandboxes' 'delete:Delete sandboxes' 'exec:Execute a command' 'ssh:Open SSH' 'info:Inspect a sandbox') ;;
        job|jobs) subcommands=('run:Run a job' 'list:List jobs' 'info:Inspect a job' 'logs:Show job logs' 'cancel:Cancel a job') ;;
        worker|workers) subcommands=('token:Manage enrollment tokens' 'list:List workers' 'info:Inspect a worker' 'cordon:Cordon a worker' 'drain:Drain a worker' 'delete:Delete a worker') ;;
        pool|pools) subcommands=('list:List pools' 'create:Create a pool' 'delete:Delete a pool') ;;
        image|images) subcommands=('list:List images' 'build:Build an image' 'inspect:Inspect an image' 'delete:Delete an image') ;;
        template|templates) subcommands=('list:List templates' 'create:Create a template' 'launch:Launch a sandbox' 'inspect:Inspect a template' 'delete:Delete a template') ;;
        completion|autocomplete) subcommands=('bash:Generate Bash completion' 'zsh:Generate Zsh completion' 'fish:Generate Fish completion' 'install:Install completion') ;;
    esac
    if (( CURRENT == 3 )) && (( ${#subcommands} )); then
        _describe 'subcommand' subcommands
        return
    fi
}

_compute "$@"
`

const fishCompletionScript = `# fish completion for compute
set -l commands sandbox vm job jobs worker workers pool pools image images template templates gui completion help

complete -c compute -n "not __fish_seen_subcommand_from $commands" -a sandbox -d "Manage isolated compute sandboxes"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a vm -d "Manage isolated compute sandboxes (alias)"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a job -d "Run and monitor distributed compute jobs"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a worker -d "Manage worker nodes and enrollments"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a pool -d "Manage worker pools"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a image -d "Manage base and custom rootfs images"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a template -d "Manage VM disk templates"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a gui -d "Launch Web UI dashboard"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a completion -d "Generate completion script"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a help -d "Show help documentation"
complete -c compute -n "__fish_seen_subcommand_from sandbox vm; and not __fish_seen_subcommand_from list create start stop delete exec ssh info" -a "list create start stop delete exec ssh info"
complete -c compute -n "__fish_seen_subcommand_from job jobs; and not __fish_seen_subcommand_from run list info logs cancel" -a "run list info logs cancel"
complete -c compute -n "__fish_seen_subcommand_from worker workers; and not __fish_seen_subcommand_from token list info cordon drain delete" -a "token list info cordon drain delete"
complete -c compute -n "__fish_seen_subcommand_from pool pools; and not __fish_seen_subcommand_from list create delete" -a "list create delete"
complete -c compute -n "__fish_seen_subcommand_from image images; and not __fish_seen_subcommand_from list build inspect delete" -a "list build inspect delete"
complete -c compute -n "__fish_seen_subcommand_from template templates; and not __fish_seen_subcommand_from list create launch inspect delete" -a "list create launch inspect delete"
`

func handleCompletionCommand(args []string) {
	if len(args) == 0 {
		printCommandError("missing shell argument for completion.", "Usage: compute completion [bash|zsh|fish|install]", "completion")
	}

	shell := strings.ToLower(args[0])
	switch shell {
	case "bash":
		fmt.Print(bashCompletionScript)
	case "zsh":
		fmt.Print(zshCompletionScript)
	case "fish":
		fmt.Print(fishCompletionScript)
	case "install":
		installShellCompletion()
	default:
		printCommandError(fmt.Sprintf("unsupported shell '%s'. Supported shells: bash, zsh, fish", shell), "Usage: compute completion [bash|zsh|fish|install]", "completion")
	}
}

func installShellCompletion() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to determine user home directory: %v\n", err)
		os.Exit(1)
	}

	shell := strings.ToLower(filepath.Base(os.Getenv("SHELL")))
	switch shell {
	case "zsh":
		dir := filepath.Join(home, ".zfunc")
		if err := writeCompletionFile(dir, "_compute", zshCompletionScript); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to install zsh completion: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Zsh completion installed to %s\n", filepath.Join(dir, "_compute"))
	case "fish":
		dir := filepath.Join(home, ".config", "fish", "completions")
		if err := writeCompletionFile(dir, "compute.fish", fishCompletionScript); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to install fish completion: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Fish completion installed to %s\n", filepath.Join(dir, "compute.fish"))
	default:
		dir := filepath.Join(home, ".local", "share", "bash-completion", "completions")
		dest := filepath.Join(dir, "compute")
		if err := writeCompletionFile(dir, "compute", bashCompletionScript); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to install bash completion: %v\n", err)
			os.Exit(1)
		}
		if err := ensureBashCompletionSource(home, dest); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to configure bash startup: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Bash completion installed to %s\n", dest)
	}
}

func writeCompletionFile(dir, name, script string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(script), 0644)
}

func ensureBashCompletionSource(home, path string) error {
	rcPath := filepath.Join(home, ".bashrc")
	const marker = "# compute shell completion"

	data, err := os.ReadFile(rcPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(data), marker) {
		return nil
	}

	entry := fmt.Sprintf("\n%s\n[ -r %q ] && . %q\n", marker, path, path)
	file, err := os.OpenFile(rcPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(entry)
	return err
}
