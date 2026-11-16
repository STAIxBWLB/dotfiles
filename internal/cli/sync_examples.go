package cli

// syncExamples holds the Example text for each runnable command under dot
// sync, dot peer and dot remote, keyed by the path below "dot". It is merged
// into helpExamples at init, so applyHelpMetadata places it with the rest.
// Commands that already carry an Example in their own file are left alone.
var syncExamples = map[string][]string{
	"sync":                        {"dot sync status", "dot sync push"},
	"sync configure":              {"dot sync configure --target local:~/Dropbox/work", "dot sync configure --propagate create,update --json"},
	"sync conflicts list":         {"dot sync conflicts list"},
	"sync conflicts prune":        {"dot sync conflicts prune --older-than 7", "dot sync conflicts prune --all --remote-only --store=peer"},
	"sync filters":                {"dot sync filters", "dot sync filters show"},
	"sync filters get":            {"dot sync filters get exclude", "dot sync filters get allow --json"},
	"sync filters reset":          {"dot sync filters reset"},
	"sync filters set":            {"dot sync filters set exclude < exclude.txt"},
	"sync filters show":           {"dot sync filters show"},
	"sync inbox clear":            {"dot sync inbox clear"},
	"sync inbox forget":           {"dot sync inbox forget notes/todo.md"},
	"sync inbox list":             {"dot sync inbox list"},
	"sync init":                   {"dot sync init"},
	"sync intake":                 {"dot sync intake", "dot sync intake --strict"},
	"sync log":                    {"dot sync log", "dot sync log --tail 50"},
	"sync names":                  {"dot sync names"},
	"sync names normalize":        {"dot sync names normalize"},
	"sync names trim":             {"dot sync names trim"},
	"sync owner":                  {"dot sync owner --set-self", "dot sync owner --rename old-mac new-mac"},
	"sync pause":                  {"dot sync pause"},
	"sync pull":                   {"dot sync pull", "dot sync pull --mode clean"},
	"sync push":                   {"dot sync push", "dot sync push --propagate=create,update,delete"},
	"sync resume":                 {"dot sync resume"},
	"sync setup":                  {"dot sync setup", "dot sync setup --push-interval 15m --owner self"},
	"sync shared add":             {"dot sync shared add projects/shared-with-partner"},
	"sync shared clear":           {"dot sync shared clear"},
	"sync shared list":            {"dot sync shared list"},
	"sync shared remove":          {"dot sync shared remove projects/shared-with-partner"},
	"sync status":                 {"dot sync status", "dot sync status --json"},
	"sync target":                 {"dot sync target", "dot sync target local:~/Dropbox/work"},
	"peer":                        {"dot peer status", "dot peer sync"},
	"peer diff":                   {"dot peer diff", "dot peer diff --list"},
	"peer doctor":                 {"dot peer doctor", "dot peer doctor --self"},
	"peer git":                    {"dot peer git status"},
	"peer git fetch":              {"dot peer git fetch", "dot peer git fetch work/dev/app"},
	"peer git realign":            {"dot peer git realign", "dot peer git realign --apply --fetch"},
	"peer git status":             {"dot peer git status", "dot peer git status --json"},
	"peer handover":               {"dot peer handover"},
	"peer home-paths":             {"dot peer home-paths get"},
	"peer home-paths get":         {"dot peer home-paths get", "dot peer home-paths get --json"},
	"peer home-paths set":         {"dot peer home-paths set < home-paths.txt"},
	"peer home-paths tracked":     {"dot peer home-paths tracked get"},
	"peer home-paths tracked get": {"dot peer home-paths tracked get --json"},
	"peer home-paths tracked set": {"dot peer home-paths tracked set < tracked.txt"},
	"peer init":                   {"dot peer init --host other-mac"},
	"peer setup":                  {"dot peer setup --interval 15m", "dot peer setup --off"},
	"peer status":                 {"dot peer status", "dot peer status --json"},
	"peer sync":                   {"dot peer sync", "dot peer sync --pull-only"},
	"peer takeover":               {"dot peer takeover"},
	"remote job check":            {"dot remote job check", "dot remote job check --json"},
	"remote job run":              {"dot remote job run -o out -i data.csv -- python3 report.py", "dot remote job run -o out --local-only -- ./build.sh"},
	"remote prune":                {"dot remote prune", "dot remote prune --yes"},
	"remote run":                  {"dot remote run -- make test", "dot remote run ~/workspace/work/app -- cargo test"},
	"remote setup":                {"dot remote setup", "dot remote setup --profile media"},
	"remote status":               {"dot remote status", "dot remote status --json"},
	"remote verify":               {"dot remote verify", "dot remote verify --dirty -- make test"},
}

func init() {
	for key, ex := range syncExamples {
		helpExamples[key] = ex
	}
}
