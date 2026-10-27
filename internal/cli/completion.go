package cli

import (
	"fmt"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
)

func cmdCompletion(conf *config.Global, args []string) int {
	if len(args) != 1 {
		fmt.Println("usage: maio completion <bash|zsh|fish>")
		return 0
	}
	switch args[0] {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	default:
		return usagef("unsupported shell %q (use bash, zsh or fish)", args[0])
	}
	return 0
}

const maioCommands = "install pull list show cp rm run serve ps stop status stats service restart boot config token doctor logs version completion help"

const bashCompletion = `# maio bash completion
_maio() {
  local cur prev words cword
  _get_comp_words_by_ref -n : cur prev words cword 2>/dev/null || {
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
  }
  local commands="` + maioCommands + `"
  if [[ $COMP_CWORD -eq 1 ]]; then
    COMPREPLY=( $(compgen -W "$commands" -- "$cur") )
    return 0
  fi
  case "${COMP_WORDS[1]}" in
    service) COMPREPLY=( $(compgen -W "start stop restart status enable disable logs" -- "$cur") ) ;;
    config)  COMPREPLY=( $(compgen -W "show path get set edit keys" -- "$cur") ) ;;
    token)   COMPREPLY=( $(compgen -W "show rotate set clear" -- "$cur") ) ;;
    completion) COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") ) ;;
    run|load|show|rm|stop|cp|pull|install) COMPREPLY=( $(compgen -W "$(maio list 2>/dev/null | awk 'NR>1 {print $1}')" -- "$cur") ) ;;
    *) COMPREPLY=( $(compgen -W "--help --json" -- "$cur") ) ;;
  esac
}
complete -F _maio maio
`

const zshCompletion = `#compdef maio
_maio() {
  local -a commands
  commands=(
    'install:install a model' 'pull:install a model' 'list:list models' 'show:show a model'
    'cp:copy a model' 'rm:remove a model' 'run:chat or run one prompt' 'serve:run the HTTP service'
    'ps:list loaded models' 'stop:unload a model' 'status:show service health' 'stats:show runtime metrics'
    'service:control the systemd service' 'restart:restart the service' 'boot:set start-on-boot'
    'config:show or edit configuration' 'token:manage the API key' 'doctor:run health checks'
    'logs:read the audit log' 'version:print version' 'completion:generate shell completion' 'help:show help'
  )
  _arguments -C \
    '1:command:->command' \
    '*::arg:->args'
  case $state in
    command) _describe 'command' commands ;;
    args)
      case $words[2] in
        service) _values 'action' start stop restart status enable disable logs ;;
        config) _values 'action' show path get set edit keys ;;
        token) _values 'action' show rotate set clear ;;
        completion) _values 'shell' bash zsh fish ;;
        *) _files ;;
      esac
      ;;
  esac
}
_maio "$@"
`

const fishCompletion = `# maio fish completion
set -l maio_commands install pull list show cp rm run serve ps stop status stats service restart boot config token doctor logs version completion help
complete -c maio -f
complete -c maio -n "not __fish_seen_subcommand_from $maio_commands" -a "$maio_commands" -d "maio command"
complete -c maio -n "__fish_seen_subcommand_from service" -a "start stop restart status enable disable logs" -d "service action"
complete -c maio -n "__fish_seen_subcommand_from config" -a "show path get set edit keys" -d "config action"
complete -c maio -n "__fish_seen_subcommand_from token" -a "show rotate set clear" -d "token action"
complete -c maio -n "__fish_seen_subcommand_from completion" -a "bash zsh fish" -d "shell"
complete -c maio -n "__fish_seen_subcommand_from run load show rm stop cp pull install" -a "(maio list 2>/dev/null | string split -f 1 ' ' | string match -v NAME)" -d "model"
`
