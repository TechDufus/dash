package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/TechDufus/dash/internal/localauth"
)

// hashPasswordUsage is the usage line of `dash hash-password`.
const hashPasswordUsage = "dash hash-password"

// errPasswordsDiffer means the two passwords typed at the terminal were not the same.
var errPasswordsDiffer = errors.New("hash-password: the passwords do not match")

// hashPasswordCommand runs `dash hash-password`: it reads a password from the terminal without
// echo (asking twice; the prompts go to stderr) or, when in is not a terminal, one line from in,
// and prints only its argon2id PHC string to out, for DASH_ADMIN_PASSWORD_HASH. The password is
// never printed, and an error never quotes it.
func hashPasswordCommand(args []string, in io.Reader, out io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("hash-password takes no arguments; usage: %s (reads the password from the terminal or stdin)", hashPasswordUsage)
	}
	var (
		password string
		err      error
	)
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		password, err = readTerminalPassword(int(f.Fd()), os.Stderr)
	} else {
		password, err = readPasswordLine(in)
	}
	if err != nil {
		return err
	}
	if err := localauth.ValidPassword(password); err != nil {
		return fmt.Errorf("hash-password: the password must be %d to %d bytes", localauth.MinPasswordBytes, localauth.MaxPasswordBytes)
	}
	phc, err := localauth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash-password: %w", err)
	}
	_, err = fmt.Fprintln(out, phc)
	return err
}

// readTerminalPassword asks for the password twice on the terminal fd without echo.
func readTerminalPassword(fd int, prompt io.Writer) (string, error) {
	read := func(label string) (string, error) {
		fmt.Fprint(prompt, label)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("hash-password: reading the password: %w", err)
		}
		return string(b), nil
	}
	first, err := read("Password: ")
	if err != nil {
		return "", err
	}
	second, err := read("Repeat password: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errPasswordsDiffer
	}
	return first, nil
}

// readPasswordLine reads the first line of in without its line ending. A line longer than
// MaxPasswordBytes is refused without reading the rest.
func readPasswordLine(in io.Reader) (string, error) {
	br := bufio.NewReaderSize(in, localauth.MaxPasswordBytes+2)
	line, err := br.ReadSlice('\n')
	switch {
	case errors.Is(err, bufio.ErrBufferFull):
		return "", fmt.Errorf("hash-password: the password must be %d to %d bytes", localauth.MinPasswordBytes, localauth.MaxPasswordBytes)
	case err != nil && !errors.Is(err, io.EOF):
		return "", fmt.Errorf("hash-password: reading stdin: %w", err)
	}
	s := strings.TrimSuffix(string(line), "\n")
	return strings.TrimSuffix(s, "\r"), nil
}
