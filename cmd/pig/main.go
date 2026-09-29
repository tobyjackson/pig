// Command pig is a small coding agent for the terminal: four tools, one
// short system prompt, and everything else added by skills, prompt
// templates, and extensions.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/docs"
	"github.com/tobyjackson/pig/modes/export"
	"github.com/tobyjackson/pig/modes/printmode"
	"github.com/tobyjackson/pig/modes/rpc"
	"github.com/tobyjackson/pig/modes/tui"
	"github.com/tobyjackson/pig/resources"
	"github.com/tobyjackson/pig/runtime"
	"github.com/tobyjackson/pig/session"
)

const version = "0.1.0"

// multi collects a repeatable flag.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pig:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Sub-commands that need no session.
	if len(args) > 0 {
		switch args[0] {
		case "docs":
			return showDocs(args[1:])
		case "login":
			return login(args[1:])
		case "sessions":
			return listSessions()
		case "models":
			return openRouterModels(args[1:])
		}
	}

	fs := flag.NewFlagSet("pig", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	var (
		print      = fs.Bool("p", false, "")
		mode       = fs.String("mode", "", "")
		modelFlag  = fs.String("model", "", "")
		provider   = fs.String("provider", "", "")
		thinking   = fs.String("thinking", "", "")
		listModels = fs.Bool("list-models", false, "")
		cont       = fs.Bool("c", false, "")
		resume     = fs.Bool("r", false, "")
		sessionArg = fs.String("session", "", "")
		forkArg    = fs.String("fork", "", "")
		noSession  = fs.Bool("no-session", false, "")
		name       = fs.String("n", "", "")
		toolsFlag  = fs.String("t", "", "")
		exclTools  = fs.String("xt", "", "")
		noTools    = fs.Bool("nt", false, "")
		sysPrompt  = fs.String("system-prompt", "", "")
		appendSys  = fs.String("append-system-prompt", "", "")
		noExt      = fs.Bool("no-extensions", false, "")
		noSkills   = fs.Bool("no-skills", false, "")
		noPrompts  = fs.Bool("no-prompt-templates", false, "")
		noCtx      = fs.Bool("nc", false, "")
		approve    = fs.Bool("a", false, "")
		noApprove  = fs.Bool("na", false, "")
		exportArg  = fs.String("export", "", "")
		showVer    = fs.Bool("v", false, "")
		showHelp   = fs.Bool("h", false, "")
		cwd        = fs.String("cwd", "", "")
	)
	var skills, prompts, exts multi
	fs.Var(&skills, "skill", "")
	fs.Var(&prompts, "prompt-template", "")
	fs.Var(&exts, "e", "")
	// Long aliases.
	fs.BoolVar(print, "print", false, "")
	fs.BoolVar(cont, "continue", false, "")
	fs.BoolVar(resume, "resume", false, "")
	fs.StringVar(name, "name", "", "")
	fs.StringVar(toolsFlag, "tools", "", "")
	fs.StringVar(exclTools, "exclude-tools", "", "")
	fs.BoolVar(noTools, "no-tools", false, "")
	fs.BoolVar(noCtx, "no-context-files", false, "")
	fs.BoolVar(approve, "approve", false, "")
	fs.BoolVar(noApprove, "no-approve", false, "")
	fs.BoolVar(showVer, "version", false, "")
	fs.BoolVar(showHelp, "help", false, "")
	fs.Var(&exts, "extension", "")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if *showHelp {
		fmt.Print(usage)
		return nil
	}
	if *showVer {
		fmt.Println("pig", version)
		return nil
	}
	if *exportArg != "" {
		out := "pig-session.html"
		if fs.NArg() > 0 {
			out = fs.Arg(0)
		}
		st, err := session.Open(*exportArg)
		if err != nil {
			return err
		}
		return export.HTML(out, "pig session", st.Messages())
	}
	if *listModels {
		reg, err := ai.NewRegistry(resources.GlobalDir())
		if err != nil {
			return err
		}
		search := strings.ToLower(fs.Arg(0))
		for _, m := range reg.Models {
			if search != "" && !strings.Contains(strings.ToLower(m.Key()+m.Name), search) {
				continue
			}
			auth := " "
			if reg.HasAuth(m.Provider) {
				auth = "✓"
			}
			fmt.Printf("%s %-42s %-24s ctx %7d  %s\n", auth, m.Key(), m.Name, m.ContextWindow, m.API)
		}
		fmt.Println("\n✓ = API key found. Add keys with `pig login <provider>` or ~/.pig/models.json.")
		return nil
	}

	runMode := "tui"
	if *print {
		runMode = "print"
	}
	if *mode != "" {
		runMode = *mode
	}
	if runMode == "json" || runMode == "rpc" {
		// no-op: both are non-interactive
	} else if runMode != "tui" && runMode != "print" {
		return fmt.Errorf("unknown --mode %q (tui, print, json, rpc)", runMode)
	}
	prompts0 := fs.Args()
	if runMode == "tui" && len(prompts0) > 0 && (fs.NArg() > 0) {
		// A prompt on the command line in interactive mode means print mode.
		runMode = "print"
	}

	modelPattern := *modelFlag
	if *provider != "" && modelPattern != "" && !strings.Contains(modelPattern, "/") {
		modelPattern = *provider + "/" + modelPattern
	} else if *provider != "" && modelPattern == "" {
		modelPattern = *provider + "/"
	}
	trust := ""
	if *approve {
		trust = "yes"
	}
	if *noApprove {
		trust = "no"
	}
	opts := runtime.Options{
		Cwd: *cwd, Model: strings.TrimSuffix(modelPattern, "/"), ThinkingLevel: *thinking, Mode: runMode,
		SessionPath: *sessionArg, Continue: *cont, ForkPath: *forkArg, Ephemeral: *noSession,
		Tools: splitList(*toolsFlag), ExcludeTools: splitList(*exclTools), NoTools: *noTools,
		SystemPrompt: *sysPrompt, AppendSystemPrompt: *appendSys,
		NoSkills: *noSkills, NoPrompts: *noPrompts, NoExtensions: *noExt, NoContextFiles: *noCtx,
		SkillPaths: skills, PromptPaths: prompts, ExtensionPaths: exts, Trust: trust,
	}
	if runMode == "tui" {
		opts.TrustPrompt = askTrust
	}
	if *resume && runMode == "tui" {
		path, ok := pickSession(opts.Cwd)
		if !ok {
			return nil
		}
		opts.SessionPath = path
	}
	if opts.Model == "" && *provider != "" {
		opts.Model = *provider + "/"
	}

	s, err := runtime.New(opts)
	if err != nil {
		return err
	}
	defer s.Close()
	if *name != "" {
		s.SetName(*name)
	}
	switch runMode {
	case "print":
		return printmode.Run(s, prompts0, false, os.Stdout)
	case "json":
		return printmode.Run(s, prompts0, true, os.Stdout)
	case "rpc":
		return rpc.Run(s, os.Stdin, os.Stdout)
	}
	quiet := s.Settings.QuietStartup != nil && *s.Settings.QuietStartup
	return tui.Run(s, quiet)
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// askTrust asks on the terminal before loading a project's .pig folder.
func askTrust(cwd string) (bool, bool) {
	fmt.Printf("This folder has project-local pig files (.pig/ or .agents/skills).\n%s\nThey can run code. Trust them? [y]es / [n]o / [a]lways for this folder / ne[v]er: ", cwd)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, false
	case "a", "always":
		return true, true
	case "v", "never":
		return false, true
	}
	return false, false
}

func pickSession(cwd string) (string, bool) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	settings := resources.LoadSettings(cwd, false)
	list := session.List(resources.SessionsDir(settings), cwd)
	if len(list) == 0 {
		fmt.Println("no saved sessions for this folder")
		return "", false
	}
	for i, info := range list {
		name := info.Name
		if name == "" {
			name = info.FirstUser
		}
		fmt.Printf("%2d  %s  %2d msgs  %s\n", i+1, info.Modified.Format("Jan 02 15:04"), info.Messages, name)
	}
	fmt.Print("pick a number (Enter cancels): ")
	var n int
	if _, err := fmt.Scanln(&n); err != nil || n < 1 || n > len(list) {
		return "", false
	}
	return list[n-1].Path, true
}

func listSessions() error {
	cwd, _ := os.Getwd()
	settings := resources.LoadSettings(cwd, false)
	for _, info := range session.List(resources.SessionsDir(settings), cwd) {
		name := info.Name
		if name == "" {
			name = info.FirstUser
		}
		fmt.Printf("%s  %s  %2d msgs  %s\n", info.ID[:8], info.Modified.Format("2006-01-02 15:04"), info.Messages, name)
	}
	return nil
}

func showDocs(args []string) error {
	if len(args) == 0 {
		fmt.Println("pig documentation topics (run `pig docs <topic>`):")
		for _, n := range docs.Names() {
			fmt.Println("  " + n)
		}
		return nil
	}
	text, ok := docs.Read(args[0])
	if !ok {
		return fmt.Errorf("no doc named %q", args[0])
	}
	fmt.Print(text)
	return nil
}

func login(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pig login <provider>   (anthropic, openai, openrouter, or a name from models.json)")
	}
	provider := strings.ToLower(args[0])
	reg, err := ai.NewRegistry(resources.GlobalDir())
	if err != nil {
		return err
	}
	known := false
	for _, p := range reg.Providers() {
		if p == provider {
			known = true
		}
	}
	if !known {
		msg := fmt.Sprintf("unknown provider %q; known: %s", provider, strings.Join(reg.Providers(), ", "))
		if c := reg.ClosestProvider(provider); c != "" {
			msg += fmt.Sprintf(". Did you mean %q? Run: pig login %s", c, c)
		}
		return fmt.Errorf("%s", msg)
	}
	fmt.Printf("Paste the API key for %s: ", provider)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	key := strings.TrimSpace(line)
	if key == "" {
		return fmt.Errorf("no key given")
	}
	if err := ai.SaveAuth(resources.GlobalDir(), provider, key); err != nil {
		return err
	}
	fmt.Println("saved to", resources.GlobalDir()+"/auth.json")
	return nil
}

const usage = `pig - a small coding agent for the terminal

Usage:
  pig [options]                 interactive
  pig [options] "prompt"        run one prompt and print the reply
  pig docs [topic]              read the built-in documentation
  pig login <provider>          save an API key
  pig sessions                  list saved sessions for this folder
  pig models [search]           list models available on openrouter.ai

Modes:
  -p, --print                   print the reply and exit
  --mode json                   print every event as a JSON line
  --mode rpc                    drive pig from another program (see: pig docs rpc)
  --export <file.jsonl> [out]   write a session as HTML

Model:
  --model <name>                provider/id, id, or part of a name
  --provider <name>             anthropic, openai, or one from models.json
  --thinking <level>            off minimal low medium high xhigh max
  --list-models [search]        show known models

Session:
  -c, --continue                resume the latest session for this folder
  -r, --resume                  pick a session to resume
  --session <path|id>           resume a specific session
  --fork <path|id>              copy a session into a new one
  --no-session                  do not save this session
  -n, --name <text>             name the session

Tools and resources:
  -t, --tools a,b               only these tools (read, bash, edit, write)
  -xt, --exclude-tools a,b      drop these tools
  -nt, --no-tools               no tools at all
  --skill <path>                add a skill (repeatable)
  --prompt-template <path>      add a prompt template (repeatable)
  -e, --extension <path>        add an extension program (repeatable)
  --no-skills  --no-prompt-templates  --no-extensions  -nc, --no-context-files
  --system-prompt <text>        replace the default system prompt
  --append-system-prompt <text> add to the system prompt
  -a, --approve                 trust this folder's .pig files
  -na, --no-approve             ignore this folder's .pig files
  --cwd <dir>                   work in another folder

  -v, --version   -h, --help

Keys: ANTHROPIC_API_KEY, OPENAI_API_KEY, OPENROUTER_API_KEY, or pig login.
Files: ~/.pig/
`

// openRouterModels prints the live OpenRouter catalogue, filtered by search.
func openRouterModels(args []string) error {
	search := strings.ToLower(strings.Join(args, " "))
	resp, err := http.Get("https://openrouter.ai/api/v1/models")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("openrouter.ai answered HTTP %d", resp.StatusCode)
	}
	var list struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			Pricing       struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return err
	}
	sort.Slice(list.Data, func(i, j int) bool { return list.Data[i].ID < list.Data[j].ID })
	n := 0
	for _, m := range list.Data {
		if search != "" && !strings.Contains(strings.ToLower(m.ID+" "+m.Name), search) {
			continue
		}
		in, _ := strconv.ParseFloat(m.Pricing.Prompt, 64)
		out, _ := strconv.ParseFloat(m.Pricing.Completion, 64)
		fmt.Printf("openrouter/%-50s ctx %8d  $%.2f/$%.2f per M\n", m.ID, m.ContextLength, in*1e6, out*1e6)
		n++
	}
	fmt.Printf("\n%d models. Use one with: pig --model openrouter/<id>   (key: pig login openrouter)\n", n)
	return nil
}
