package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/texhik/conclave/internal/tool"
)

// cliAsker prompts the user on the terminal. It is only created when stdin is a
// character device; otherwise nil is returned and questions are non-interactive.
type cliAsker struct {
	in  *bufio.Reader
	out io.Writer
}

func newCLIAsker() tool.Asker {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	return &cliAsker{in: bufio.NewReader(os.Stdin), out: os.Stderr}
}

func (a *cliAsker) Ask(ctx context.Context, q tool.Question) (tool.Answer, error) {
	select {
	case <-ctx.Done():
		return tool.Answer{}, ctx.Err()
	default:
	}

	if q.Header != "" {
		fmt.Fprintf(a.out, "\n┌─ %s\n", q.Header)
	} else {
		fmt.Fprintln(a.out, "\n┌─ question")
	}
	fmt.Fprintf(a.out, "│ %s\n", q.Question)
	for i, opt := range q.Options {
		line := fmt.Sprintf("%d. %s", i+1, opt.Label)
		if opt.Description != "" {
			line += " — " + opt.Description
		}
		fmt.Fprintf(a.out, "│ %s\n", line)
	}
	if q.AllowCustom {
		fmt.Fprintln(a.out, "│ c. own answer")
	}
	hint := "1"
	if q.Multiple {
		hint = "1,2"
	}
	if q.AllowCustom {
		hint += " or text"
	}
	fmt.Fprintf(a.out, "└ choose [%s] (Enter to skip): ", hint)

	line, err := a.in.ReadString('\n')
	if err != nil && len(line) == 0 {
		return tool.Answer{}, tool.ErrNoInteractiveUser
	}
	return parseAnswer(strings.TrimSpace(line), q)
}

func parseAnswer(input string, q tool.Question) (tool.Answer, error) {
	var ans tool.Answer
	if input == "" {
		return ans, nil
	}
	tokens := strings.Split(input, ",")
	custom := []string{}
	for _, raw := range tokens {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}
		if n, err := strconv.Atoi(token); err == nil {
			if n < 1 || n > len(q.Options) {
				return ans, fmt.Errorf("choice %d is out of range", n)
			}
			ans.Selected = append(ans.Selected, q.Options[n-1].Label)
			continue
		}
		if label := matchLabel(token, q.Options); label != "" {
			ans.Selected = append(ans.Selected, label)
			continue
		}
		if q.AllowCustom || strings.EqualFold(token, "c") {
			custom = append(custom, token)
			continue
		}
		return ans, fmt.Errorf("unrecognized choice %q", token)
	}
	if len(ans.Selected) > 1 && !q.Multiple {
		ans.Selected = ans.Selected[:1]
	}
	ans.Custom = strings.Join(custom, ", ")
	return ans, nil
}

func matchLabel(token string, options []tool.Option) string {
	lower := strings.ToLower(token)
	for _, opt := range options {
		if strings.ToLower(opt.Label) == lower {
			return opt.Label
		}
	}
	for _, opt := range options {
		if strings.HasPrefix(strings.ToLower(opt.Label), lower) {
			return opt.Label
		}
	}
	return ""
}
