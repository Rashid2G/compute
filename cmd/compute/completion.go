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

    local commands="auth sandbox vm job jobs worker workers pool pools image images template templates keys key ssh-keys file files cp scp process processes ps exec ssh url expose ports unexpose doctor check network net version gui web ui completion autocomplete help"

    local auth_subcommands="login signup whoami me keys logout"
    local auth_keys_subcommands="list create revoke delete rm"
    local sandbox_subcommands="list ls create start run stop save-state snapshot resume delete rm protect unprotect network-isolation images templates file cp process ps info inspect show url expose ports unexpose exec ssh"
    local job_subcommands="run list ls info logs cancel"
    local worker_subcommands="token list ls info cordon drain delete rm"
    local pool_subcommands="list ls create delete rm"
    local image_subcommands="list ls build create inspect show delete rm"
    local template_subcommands="list ls create launch inspect show delete rm"
    local keys_subcommands="list ls add create new apply reconcile repair update set edit set-default show info get delete rm remove"
    local file_subcommands="read cat write list ls mkdir remove rm delete"
    local process_subcommands="list ls ps start run kill signal"
    local network_subcommands="status info show list ls inspect prune"
    local completion_subcommands="bash zsh fish install"

    if [[ $cword -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "$commands" -- "$cur") )
        return 0
    fi

    # Dynamic flag completion
    case "$prev" in
        --image)
            local images=$("$completion_command" __complete images 2>/dev/null)
            COMPREPLY=( $(compgen -W "$images" -- "$cur") )
            return 0
            ;;
        --template)
            local tmpls=$("$completion_command" __complete templates 2>/dev/null)
            COMPREPLY=( $(compgen -W "$tmpls" -- "$cur") )
            return 0
            ;;
        --pool)
            local pools=$("$completion_command" __complete pools 2>/dev/null)
            COMPREPLY=( $(compgen -W "$pools" -- "$cur") )
            return 0
            ;;
        --ssh-key)
            local keys=$("$completion_command" __complete keys 2>/dev/null)
            COMPREPLY=( $(compgen -W "$keys" -- "$cur") )
            return 0
            ;;
        --network-isolation)
            COMPREPLY=( $(compgen -W "account isolated" -- "$cur") )
            return 0
            ;;
        --arch)
            COMPREPLY=( $(compgen -W "amd64 arm64" -- "$cur") )
            return 0
            ;;
        --tier)
            COMPREPLY=( $(compgen -W "micro small medium large xlarge" -- "$cur") )
            return 0
            ;;
    esac

    local cmd="${words[1]}"
    case "$cmd" in
        auth)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$auth_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -eq 3 && "${words[2]}" == "keys" ]]; then
                COMPREPLY=( $(compgen -W "$auth_keys_subcommands" -- "$cur") )
                return 0
            fi
            ;;
        exec)
            local vms=$("$completion_command" __complete vms 2>/dev/null)
            COMPREPLY=( $(compgen -W "$vms --user --timeout --no-stream --json --help" -- "$cur") )
            return 0
            ;;
        ssh)
            local vms=$("$completion_command" __complete vms 2>/dev/null)
            COMPREPLY=( $(compgen -W "$vms --user -u --password -p -t -T --help" -- "$cur") )
            return 0
            ;;
        url)
            local vms=$("$completion_command" __complete vms 2>/dev/null)
            COMPREPLY=( $(compgen -W "$vms --json --port --user -u --verbose -v --help" -- "$cur") )
            return 0
            ;;
        expose)
            local vms=$("$completion_command" __complete vms 2>/dev/null)
            COMPREPLY=( $(compgen -W "$vms --public --private --subdomain --json --help" -- "$cur") )
            return 0
            ;;
        ports)
            local vms=$("$completion_command" __complete vms 2>/dev/null)
            COMPREPLY=( $(compgen -W "$vms --json --help" -- "$cur") )
            return 0
            ;;
        unexpose)
            local vms=$("$completion_command" __complete vms 2>/dev/null)
            COMPREPLY=( $(compgen -W "$vms --help" -- "$cur") )
            return 0
            ;;
        sandbox|vm)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$sandbox_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -ge 3 ]]; then
                case "${words[2]}" in
                    create)
                        COMPREPLY=( $(compgen -W "--name --tier --vcpus --memory --disk --image --template --ssh-key --ssh-password --command --build-command --user --network-isolation --isolated --protected --start --boot --help" -- "$cur") )
                        ;;
                    start|run|stop|save-state|snapshot|resume|delete|rm|info|inspect|show|protect|unprotect)
                        local vms=$("$completion_command" __complete vms 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$vms --help" -- "$cur") )
                        ;;
                    network-isolation)
                        if [[ $cword -eq 3 ]]; then
                            local vms=$("$completion_command" __complete vms 2>/dev/null)
                            COMPREPLY=( $(compgen -W "$vms" -- "$cur") )
                        elif [[ $cword -eq 4 ]]; then
                            COMPREPLY=( $(compgen -W "account isolated" -- "$cur") )
                        fi
                        ;;
                    exec)
                        local vms=$("$completion_command" __complete vms 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$vms --user --timeout --no-stream --json --help" -- "$cur") )
                        ;;
                    ssh)
                        local vms=$("$completion_command" __complete vms 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$vms --user -u --password -p -t -T --help" -- "$cur") )
                        ;;
                    url)
                        local vms=$("$completion_command" __complete vms 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$vms --json --port --user -u --verbose -v --help" -- "$cur") )
                        ;;
                    expose)
                        local vms=$("$completion_command" __complete vms 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$vms --public --private --subdomain --json --help" -- "$cur") )
                        ;;
                    ports)
                        local vms=$("$completion_command" __complete vms 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$vms --json --help" -- "$cur") )
                        ;;
                    unexpose)
                        local vms=$("$completion_command" __complete vms 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$vms --help" -- "$cur") )
                        ;;
                    file)
                        if [[ $cword -eq 3 ]]; then
                            COMPREPLY=( $(compgen -W "$file_subcommands" -- "$cur") )
                        elif [[ $cword -eq 4 ]]; then
                            local vms=$("$completion_command" __complete vms 2>/dev/null)
                            COMPREPLY=( $(compgen -W "$vms" -- "$cur") )
                        fi
                        ;;
                    process|ps)
                        if [[ $cword -eq 3 ]]; then
                            COMPREPLY=( $(compgen -W "$process_subcommands" -- "$cur") )
                        elif [[ $cword -eq 4 ]]; then
                            local vms=$("$completion_command" __complete vms 2>/dev/null)
                            COMPREPLY=( $(compgen -W "$vms" -- "$cur") )
                        fi
                        ;;
                    cp)
                        local vms=$("$completion_command" __complete vms 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$vms" -- "$cur") )
                        ;;
                esac
                return 0
            fi
            ;;
        file|files)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$file_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -eq 3 ]]; then
                local vms=$("$completion_command" __complete vms 2>/dev/null)
                COMPREPLY=( $(compgen -W "$vms" -- "$cur") )
                return 0
            fi
            ;;
        process|processes|ps)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$process_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -eq 3 ]]; then
                local vms=$("$completion_command" __complete vms 2>/dev/null)
                COMPREPLY=( $(compgen -W "$vms" -- "$cur") )
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
                    run)
                        COMPREPLY=( $(compgen -W "--image --pool --arch --vcpus --mem --files --cwd --timeout --follow --help" -- "$cur") )
                        ;;
                    info|logs|cancel)
                        local jobs=$("$completion_command" __complete jobs 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$jobs --help" -- "$cur") )
                        ;;
                    list|ls)
                        COMPREPLY=( $(compgen -W "--help" -- "$cur") )
                        ;;
                esac
                return 0
            fi
            ;;
        worker|workers)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$worker_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -ge 3 ]]; then
                case "${words[2]}" in
                    token)
                        if [[ $cword -eq 3 ]]; then
                            COMPREPLY=( $(compgen -W "create" -- "$cur") )
                        else
                            COMPREPLY=( $(compgen -W "--pool --name --help" -- "$cur") )
                        fi
                        ;;
                    info|cordon|drain|delete|rm)
                        local workers=$("$completion_command" __complete workers 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$workers --help" -- "$cur") )
                        ;;
                    list|ls)
                        COMPREPLY=( $(compgen -W "--help" -- "$cur") )
                        ;;
                esac
                return 0
            fi
            ;;
        pool|pools)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$pool_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -ge 3 ]]; then
                case "${words[2]}" in
                    delete|rm)
                        local pools=$("$completion_command" __complete pools 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$pools --help" -- "$cur") )
                        ;;
                    list|ls|create)
                        COMPREPLY=( $(compgen -W "--help" -- "$cur") )
                        ;;
                esac
                return 0
            fi
            ;;
        image|images)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$image_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -ge 3 ]]; then
                case "${words[2]}" in
                    build|create)
                        COMPREPLY=( $(compgen -W "--name --packages --base --size --desc --help" -- "$cur") )
                        ;;
                    inspect|show|delete|rm)
                        local images=$("$completion_command" __complete images 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$images --help" -- "$cur") )
                        ;;
                    list|ls)
                        COMPREPLY=( $(compgen -W "--help" -- "$cur") )
                        ;;
                esac
                return 0
            fi
            ;;
        template|templates)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$template_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -ge 3 ]]; then
                case "${words[2]}" in
                    create)
                        COMPREPLY=( $(compgen -W "--vm --desc --name --tier --help" -- "$cur") )
                        ;;
                    launch|inspect|show|delete|rm)
                        local tmpls=$("$completion_command" __complete templates 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$tmpls --help" -- "$cur") )
                        ;;
                    list|ls)
                        COMPREPLY=( $(compgen -W "--help" -- "$cur") )
                        ;;
                esac
                return 0
            fi
            ;;
        keys|key|ssh-keys)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$keys_subcommands" -- "$cur") )
                return 0
            fi
            if [[ $cword -ge 3 ]]; then
                case "${words[2]}" in
                    apply|show|info|get|delete|rm|remove|update|set-default)
                        local keys=$("$completion_command" __complete keys 2>/dev/null)
                        COMPREPLY=( $(compgen -W "$keys --help" -- "$cur") )
                        ;;
                esac
                return 0
            fi
            ;;
        network|net)
            if [[ $cword -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$network_subcommands" -- "$cur") )
                return 0
            fi
            ;;
        doctor|check)
            COMPREPLY=( $(compgen -W "--fix --verbose --json --help" -- "$cur") )
            return 0
            ;;
        version)
            COMPREPLY=( $(compgen -W "--client --short --json --help" -- "$cur") )
            return 0
            ;;
        gui|web|ui)
            COMPREPLY=( $(compgen -W "--port --addr --daemon --help" -- "$cur") )
            return 0
            ;;
        scp)
            local vms=$("$completion_command" __complete vms 2>/dev/null)
            COMPREPLY=( $(compgen -W "$vms -r --recursive --help" -- "$cur") )
            return 0
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
        'auth:User accounts, authentication and API keys'
        'sandbox:Manage isolated compute sandboxes'
        'vm:Manage isolated compute sandboxes (alias for sandbox)'
        'job:Run and monitor distributed compute jobs'
        'worker:Manage worker nodes and enrollments'
        'pool:Manage worker pools'
        'image:Manage base and custom rootfs images'
        'template:Manage VM disk templates'
        'keys:Manage saved SSH public keys'
        'file:Read, write, list, mkdir, and remove files in sandboxes'
        'cp:Copy files between local host and sandboxes'
        'scp:Copy files between local host and sandboxes (scp-style)'
        'process:List, start, and kill processes inside sandboxes'
        'ps:Manage processes (alias for process)'
        'exec:Execute commands inside a sandbox with streaming output'
        'ssh:Connect to sandbox via SSH'
        'url:Print a sandbox URL or SSH target'
        'expose:Publish a sandbox port on daemon ingress URL'
        'ports:List guest ports exposed on a sandbox'
        'unexpose:Remove a sandbox port exposure'
        'doctor:Print daemon readiness checks with remediation'
        'network:Inspect daemon network allocations and bridge'
        'version:Display version, build, and release information'
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
        auth)
            subcommands=('login:Log in to user account' 'signup:Create a new account' 'whoami:Show current user info' 'keys:Manage user API keys' 'logout:Log out from account')
            ;;
        sandbox|vm)
            subcommands=(
                'list:List sandboxes'
                'create:Create a new sandbox'
                'start:Start sandboxes'
                'stop:Stop sandboxes'
                'save-state:Save sandbox snapshot'
                'resume:Resume sandbox from snapshot'
                'delete:Delete sandboxes'
                'protect:Enable deletion protection'
                'unprotect:Disable deletion protection'
                'network-isolation:Switch network isolation'
                'info:Inspect sandbox details'
                'url:Print sandbox URL or SSH target'
                'expose:Publish a port on daemon ingress'
                'ports:List exposed ports'
                'unexpose:Remove port exposure'
                'file:Manage files inside sandbox'
                'cp:Copy files'
                'process:Manage processes inside sandbox'
                'exec:Execute a command in sandbox'
                'ssh:Open SSH session'
            )
            ;;
        job|jobs)
            subcommands=('run:Run a job' 'list:List jobs' 'info:Inspect a job' 'logs:Show job logs' 'cancel:Cancel a job')
            ;;
        worker|workers)
            subcommands=('token:Manage enrollment tokens' 'list:List workers' 'info:Inspect a worker' 'cordon:Cordon a worker' 'drain:Drain a worker' 'delete:Delete a worker')
            ;;
        pool|pools)
            subcommands=('list:List pools' 'create:Create a pool' 'delete:Delete a pool')
            ;;
        image|images)
            subcommands=('list:List images' 'build:Build a custom image' 'inspect:Inspect an image' 'delete:Delete an image')
            ;;
        template|templates)
            subcommands=('list:List templates' 'create:Create a template' 'launch:Launch a sandbox from template' 'inspect:Inspect a template' 'delete:Delete a template')
            ;;
        keys|key|ssh-keys)
            subcommands=('list:List SSH keys' 'add:Add a public key' 'apply:Apply key to sandbox' 'reconcile:Reconcile sandbox SSH keys' 'update:Update key metadata' 'set-default:Set default SSH key' 'show:Show key details' 'delete:Delete an SSH key')
            ;;
        file|files)
            subcommands=('read:Read file contents' 'write:Write file data' 'list:List directory contents' 'mkdir:Create directory' 'remove:Remove file or directory')
            ;;
        process|processes|ps)
            subcommands=('list:List running processes' 'start:Start detached process' 'kill:Send signal to process')
            ;;
        network|net)
            subcommands=('status:Show network configuration' 'list:List allocations' 'inspect:Inspect sandbox networking' 'prune:Prune orphaned allocations')
            ;;
        completion|autocomplete)
            subcommands=('bash:Generate Bash completion' 'zsh:Generate Zsh completion' 'fish:Generate Fish completion' 'install:Install shell completion')
            ;;
    esac

    if (( CURRENT == 3 )) && (( ${#subcommands} )); then
        _describe 'subcommand' subcommands
        return
    fi
}

_compute "$@"
`

const fishCompletionScript = `# fish completion for compute
set -l commands auth sandbox vm job jobs worker workers pool pools image images template templates keys key ssh-keys file files cp scp process processes ps exec ssh url expose ports unexpose doctor check network net version gui web ui completion autocomplete help

complete -c compute -n "not __fish_seen_subcommand_from $commands" -a auth -d "User accounts, authentication and API keys"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a sandbox -d "Manage isolated compute sandboxes"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a vm -d "Manage isolated compute sandboxes (alias)"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a job -d "Run and monitor distributed compute jobs"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a worker -d "Manage worker nodes and enrollments"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a pool -d "Manage worker pools"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a image -d "Manage base and custom rootfs images"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a template -d "Manage VM disk templates"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a keys -d "Manage saved SSH public keys"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a file -d "Manage files inside sandboxes"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a cp -d "Copy files between host and sandboxes"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a scp -d "Copy files (scp-style)"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a process -d "Manage processes in sandboxes"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a exec -d "Execute commands inside a sandbox"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a ssh -d "Connect to sandbox via SSH"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a url -d "Print sandbox URL or SSH target"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a expose -d "Publish sandbox port on ingress URL"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a ports -d "List guest ports exposed on sandbox"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a unexpose -d "Remove sandbox port exposure"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a doctor -d "Print daemon readiness checks"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a network -d "Inspect daemon network"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a version -d "Display version information"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a gui -d "Launch Web UI dashboard"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a completion -d "Generate completion script"
complete -c compute -n "not __fish_seen_subcommand_from $commands" -a help -d "Show help documentation"

complete -c compute -n "__fish_seen_subcommand_from auth" -a "login signup whoami keys logout"
complete -c compute -n "__fish_seen_subcommand_from sandbox vm" -a "list ls create start run stop save-state snapshot resume delete rm protect unprotect network-isolation info inspect show url expose ports unexpose file cp process ps exec ssh"
complete -c compute -n "__fish_seen_subcommand_from job jobs" -a "run list ls info logs cancel"
complete -c compute -n "__fish_seen_subcommand_from worker workers" -a "token list ls info cordon drain delete rm"
complete -c compute -n "__fish_seen_subcommand_from pool pools" -a "list ls create delete rm"
complete -c compute -n "__fish_seen_subcommand_from image images" -a "list ls build create inspect show delete rm"
complete -c compute -n "__fish_seen_subcommand_from template templates" -a "list ls create launch inspect show delete rm"
complete -c compute -n "__fish_seen_subcommand_from keys key ssh-keys" -a "list ls add create apply reconcile repair update set-default show info get delete rm remove"
complete -c compute -n "__fish_seen_subcommand_from file files" -a "read cat write list ls mkdir remove rm delete"
complete -c compute -n "__fish_seen_subcommand_from process processes ps" -a "list ls ps start run kill signal"
complete -c compute -n "__fish_seen_subcommand_from network net" -a "status info show list ls inspect prune"
complete -c compute -n "__fish_seen_subcommand_from completion autocomplete" -a "bash zsh fish install"
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
