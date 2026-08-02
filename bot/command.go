package main

import "strings"

// Команды бота. Короткие псевдонимы есть у каждой: команды набирают с
// телефона, и /v вместо /versions экономит больше, чем кажется.
const (
	CmdHelp      = "help"
	CmdStatus    = "status"
	CmdVersions  = "versions"
	CmdIncidents = "incidents"
)

var aliases = map[string]string{
	"help":      CmdHelp,
	"start":     CmdHelp,
	"h":         CmdHelp,
	"status":    CmdStatus,
	"s":         CmdStatus,
	"state":     CmdStatus,
	"versions":  CmdVersions,
	"version":   CmdVersions,
	"v":         CmdVersions,
	"incidents": CmdIncidents,
	"i":         CmdIncidents,
	"log":       CmdIncidents,
}

// parseCommand достаёт команду из текста сообщения.
//
// Возвращает пустую строку, если это не команда вовсе или команда адресована
// другому боту: в группе с несколькими ботами сообщение «/status@other_bot»
// приходит всем, и отвечать на чужое обращение — верный способ мешать.
//
// self передаётся без «@» и сравнивается без учёта регистра, потому что
// Telegram сохраняет регистр так, как его набрал отправитель.
func parseCommand(text, self string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	word := strings.Fields(text)[0]
	word = strings.TrimPrefix(word, "/")
	if name, at, found := strings.Cut(word, "@"); found {
		if self != "" && !strings.EqualFold(at, self) {
			return ""
		}
		word = name
	}
	return strings.ToLower(word)
}

// resolveCommand приводит псевдоним к канонической команде.
// Пустая строка означает «не наша команда».
func resolveCommand(word string) string {
	return aliases[word]
}
