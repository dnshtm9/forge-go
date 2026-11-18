// The trial's seeded identity table: the suite's fixed scenario tokens (scenario.ts's exact values).
// Only the constants the forge's surfaces read live here: the viewer's login and id, the 12 users,
// and which repository names the pagination check creates.
package main

// The scenario seed's hex (unused at runtime; kept for provenance).
const scenarioSeedHex = "6a09e667f3bcc909"

type scenarioUser struct {
	id    int
	login string
	name  string
	email string
}

var scenarioUsers = []scenarioUser{
	{1000, "adaiversen", "ada iversen", "adaiversen@example.invalid"},
	{1001, "quinnkeller", "quinn keller", "quinnkeller@example.invalid"},
	{1002, "olgabauer", "olga bauer", "olgabauer@example.invalid"},
	{1003, "dmitriquist", "dmitri quist", "dmitriquist@example.invalid"},
	{1004, "adanovak", "ada novak", "adanovak@example.invalid"},
	{1005, "zanerossi", "zane rossi", "zanerossi@example.invalid"},
	{1006, "pavelcosta", "pavel costa", "pavelcosta@example.invalid"},
	{1007, "wrenkeller", "wren keller", "wrenkeller@example.invalid"},
	{1008, "coraquist", "cora quist", "coraquist@example.invalid"},
	{1009, "elenaulrich", "elena ulrich", "elenaulrich@example.invalid"},
	{1010, "tesssato", "tess sato", "tesssato@example.invalid"},
	{1011, "svenrossi", "sven rossi", "svenrossi@example.invalid"},
}

// The viewer: the token's login (scenario.ts USERS[0]).
func scenarioViewerLogin() string { return scenarioUsers[0].login }

func scenarioViewerID() int { return scenarioUsers[0].id }
