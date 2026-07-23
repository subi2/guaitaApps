package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed web/shell.html
var shellFS embed.FS

//go:embed web/index.html
var indexFS embed.FS

//go:embed web/guaita-bridge.js
var bridgeFS embed.FS

type appEntry struct {
	File       string `json:"file"`
	Nom        string `json:"nom"`
	Descripcio string `json:"descripcio"`
	Apartat    string `json:"apartat"`
	Ordre      int    `json:"ordre"`
	Mida       int64  `json:"mida"`
	Falta      bool   `json:"falta"`
	Ajuda      bool   `json:"ajuda"`
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
var wsRe = regexp.MustCompile(`\s+`)
var numPrefixRe = regexp.MustCompile(`^(\d+)`)
var cleanPrefixRe = regexp.MustCompile(`^\d+\s*[-_.]?\s*`)
// Ajuda: "HLP…" o bé "<num> HLP…" amb separadors espai, guió baix, guió o punt
// (p. ex. "130 HLP- Nom", "110_HLP-_Nom", "HLP_Nom").
var helpPrefixRe = regexp.MustCompile(`(?i)^(\d+[\s_.-]+)?hlp([\s_.-]|$)`)
// Neteja del prefix d'ajuda sencer, separadors inclosos.
var helpCleanRe = regexp.MustCompile(`(?i)^(\d+[\s_.-]+)?hlp[\s_.-]*`)

// isHelp indica si el fitxer és una pàgina d'ajuda (prefix HLP).
func isHelp(name string) bool {
	return helpPrefixRe.MatchString(name)
}

// stripHelpPrefix treu el prefix HLP del nom del fitxer per mostrar-lo net.
func stripHelpPrefix(name string) string {
	name = strings.TrimSuffix(name, filepath.Ext(name))
	if cleaned := helpCleanRe.ReplaceAllString(name, ""); cleaned != "" {
		return cleaned
	}
	return name
}

// leadingNumber extreu un prefix numèric del nom del fitxer ("01_x.html" -> 1).
func leadingNumber(name string) (int, bool) {
	m := numPrefixRe.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// baseDir retorna la carpeta on hi ha l'executable (o el cwd si falla).
func baseDir() string {
	exe, err := os.Executable()
	if err == nil {
		return filepath.Dir(exe)
	}
	wd, _ := os.Getwd()
	return wd
}

func appsDir() string {
	dir := filepath.Join(baseDir(), "apps")
	os.MkdirAll(dir, 0o755)
	return dir
}

func readTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return filepath.Base(path)
	}
	defer f.Close()
	buf := make([]byte, 8000)
	n, _ := f.Read(buf)
	m := titleRe.FindSubmatch(buf[:n])
	if m != nil {
		t := strings.TrimSpace(wsRe.ReplaceAllString(string(m[1]), " "))
		if t != "" {
			return t
		}
	}
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	if cleaned := cleanPrefixRe.ReplaceAllString(name, ""); cleaned != "" {
		return cleaned
	}
	return name
}

func readMeta(dir string) map[string]struct {
	Nom          string `json:"nom"`
	Descripcio   string `json:"descripcio"`
	Apartat      string `json:"apartat"`
	Ordre        *int   `json:"ordre"`
	OrdreApartat *int   `json:"ordreApartat"`
	App          string `json:"app"`
} {
	out := map[string]struct {
		Nom          string `json:"nom"`
		Descripcio   string `json:"descripcio"`
		Apartat      string `json:"apartat"`
		Ordre        *int   `json:"ordre"`
		OrdreApartat *int   `json:"ordreApartat"`
		App          string `json:"app"`
	}{}
	data, err := os.ReadFile(filepath.Join(dir, "guaita-apps.json"))
	if err != nil {
		return out
	}
	json.Unmarshal(data, &out)
	return out
}

func scanApps() []appEntry {
	dir := appsDir()
	meta := readMeta(dir)
	entries, _ := os.ReadDir(dir)

	// Ordre relatiu dels apartats: mínim "ordreApartat" declarat per a cada apartat.
	apartatOrdre := map[string]int{}
	for _, m := range meta {
		if m.OrdreApartat != nil {
			if cur, ok := apartatOrdre[m.Apartat]; !ok || *m.OrdreApartat < cur {
				apartatOrdre[m.Apartat] = *m.OrdreApartat
			}
		}
	}

	present := map[string]bool{}
	var apps []appEntry
	var helpFiles []string
	helpSize := map[string]int64{}
	helpTitol := map[string]string{}
	ordreInfo := map[string]appEntry{}      // file de l'app -> app (per heretar via meta.App)
	ordreInfoPerNum := map[int]appEntry{}   // prefix numèric -> app (fallback per prefix compartit)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		low := strings.ToLower(e.Name())
		if !strings.HasSuffix(low, ".html") && !strings.HasSuffix(low, ".htm") {
			continue
		}
		present[e.Name()] = true
		full := filepath.Join(dir, e.Name())
		info, _ := e.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}
		if isHelp(e.Name()) {
			helpFiles = append(helpFiles, e.Name())
			helpSize[e.Name()] = size
			helpTitol[e.Name()] = readTitle(full)
			continue
		}
		a := appEntry{File: e.Name(), Nom: readTitle(full), Ordre: 999, Mida: size}
		if n, ok := leadingNumber(e.Name()); ok {
			a.Ordre = n
		}
		if m, ok := meta[e.Name()]; ok {
			if m.Nom != "" {
				a.Nom = m.Nom
			}
			a.Descripcio = m.Descripcio
			a.Apartat = m.Apartat
			if m.Ordre != nil {
				a.Ordre = *m.Ordre
			}
		}
		apps = append(apps, a)
		ordreInfo[e.Name()] = a
		if n, ok := leadingNumber(e.Name()); ok {
			if _, exists := ordreInfoPerNum[n]; !exists {
				ordreInfoPerNum[n] = a
			}
		}
	}

	// Apps declarades al JSON que encara no són a la carpeta: es mostren com a pendents.
	for file, m := range meta {
		if present[file] {
			continue
		}
		low := strings.ToLower(file)
		if !strings.HasSuffix(low, ".html") && !strings.HasSuffix(low, ".htm") {
			continue
		}
		if isHelp(file) {
			continue
		}
		a := appEntry{File: file, Nom: m.Nom, Descripcio: m.Descripcio, Apartat: m.Apartat, Ordre: 999, Falta: true}
		if a.Nom == "" {
			a.Nom = strings.TrimSuffix(file, filepath.Ext(file))
		}
		if n, ok := leadingNumber(file); ok {
			a.Ordre = n
		}
		if m.Ordre != nil {
			a.Ordre = *m.Ordre
		}
		apps = append(apps, a)
	}

	// Ajudes (prefix "<num> HLP-"): porten el seu propi número ordinal, com les
	// apps, de manera que es llisten igual (amb separadors de centena). L'apartat
	// s'hereta de l'app amb el mateix número, o via "app" declarat al JSON.
	for _, file := range helpFiles {
		nom := helpTitol[file]
		if nom == "" || nom == strings.TrimSuffix(file, filepath.Ext(file)) {
			nom = stripHelpPrefix(file)
		}
		h := appEntry{File: file, Nom: nom, Mida: helpSize[file], Ajuda: true, Ordre: 999}
		if n, ok := leadingNumber(file); ok {
			h.Ordre = n
		}
		if m, ok := meta[file]; ok {
			if m.Nom != "" {
				h.Nom = m.Nom
			}
			if m.Ordre != nil {
				h.Ordre = *m.Ordre
			}
			if m.Apartat != "" {
				h.Apartat = m.Apartat
			}
			if m.App != "" {
				if font, ok := ordreInfo[m.App]; ok && h.Apartat == "" {
					h.Apartat = font.Apartat
				}
			}
		}
		if h.Apartat == "" {
			if font, ok := ordreInfoPerNum[h.Ordre]; ok {
				h.Apartat = font.Apartat
			}
		}
		// L'ajuda general del launcher va sempre al final de tot, rere el separador.
		if strings.EqualFold(file, "HLP_Guaita_apps-ajuda.html") {
			h.Ordre = 100000
			h.Apartat = ""
		}
		apps = append(apps, h)
	}

	sort.SliceStable(apps, func(i, j int) bool {
		oi, oj := apartatOrdre[apps[i].Apartat], apartatOrdre[apps[j].Apartat]
		if oi != oj {
			return oi < oj
		}
		if apps[i].Apartat != apps[j].Apartat {
			return strings.ToLower(apps[i].Apartat) < strings.ToLower(apps[j].Apartat)
		}
		if apps[i].Ordre != apps[j].Ordre {
			return apps[i].Ordre < apps[j].Ordre
		}
		return strings.ToLower(apps[i].Nom) < strings.ToLower(apps[j].Nom)
	})
	return apps
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		edge := []string{
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		}
		chrome := []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
		}
		for _, p := range append(edge, chrome...) {
			if _, err := os.Stat(p); err == nil {
				exec.Command(p, "--app="+url, "--new-window").Start()
				return
			}
		}
		// Fallback: navegador per defecte.
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		exec.Command("open", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

// normalitzaNom redueix un nom de fitxer a la seva essència comparable:
// minúscules, sense extensió, només lletres i xifres, sense el número ordinal
// inicial ni el marcador HLP. Així "HLP GuaitaFitxersAjuda.html" i
// "110_HLP-_GuaitaFitxersAjuda.html" esdevenen tots dos "guaitafitxersajuda".
var nomesAlfanum = regexp.MustCompile(`[^a-z0-9]+`)
var digitsInici = regexp.MustCompile(`^[0-9]+`)

func normalitzaNom(nom string) string {
	n := strings.ToLower(strings.TrimSuffix(nom, filepath.Ext(nom)))
	n = nomesAlfanum.ReplaceAllString(n, "")
	n = digitsInici.ReplaceAllString(n, "")
	n = strings.TrimPrefix(n, "hlp")
	return n
}

// resolTolerant busca dins d'apps el fitxer HTML que millor encaixa amb el nom
// demanat: primer per coincidència exacta del nom normalitzat, i si no, el més
// proper per contenció. Retorna "" si no hi ha cap candidat raonable.
func resolTolerant(demanat string) string {
	objectiu := normalitzaNom(demanat)
	if objectiu == "" {
		return ""
	}
	entries, err := os.ReadDir(appsDir())
	if err != nil {
		return ""
	}
	millor, millorDif := "", 1<<30
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		low := strings.ToLower(e.Name())
		if !strings.HasSuffix(low, ".html") && !strings.HasSuffix(low, ".htm") {
			continue
		}
		n := normalitzaNom(e.Name())
		if n == "" {
			continue
		}
		if n == objectiu {
			return e.Name() // coincidència exacta: la millor possible
		}
		if strings.Contains(n, objectiu) || strings.Contains(objectiu, n) {
			dif := len(n) - len(objectiu)
			if dif < 0 {
				dif = -dif
			}
			if dif < millorDif {
				millor, millorDif = e.Name(), dif
			}
		}
	}
	return millor
}

const paginaAjudaNoTrobada = `<!doctype html><html lang="ca"><head><meta charset="utf-8">
<title>Ajuda no trobada</title><style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
font-family:"Segoe UI",system-ui,sans-serif;background:#0b322e;color:#eaf3f1;text-align:center;padding:24px}
.caixa{max-width:520px}
.q{font-size:52px;color:#CDF500;margin-bottom:10px}
h1{font-size:20px;font-weight:600;margin:0 0 10px}
p{color:#9fc3bd;line-height:1.6;margin:8px 0}
b{color:#CDF500;font-weight:600}
</style></head><body><div class="caixa">
<div class="q">?</div>
<h1>Aquesta ajuda contextual no s'ha trobat</h1>
<p>El fitxer d'ajuda que buscava aquesta app no és a la carpeta <b>apps</b> amb cap nom reconeixible.</p>
<p>Intenta localitzar-la tu mateix a la pestanya <b>«l'ajuda»</b> de la barra lateral de Guaita apps.</p>
</div></body></html>`

func main() {
	shell, _ := shellFS.ReadFile("web/shell.html")
	portada, _ := indexFS.ReadFile("web/index.html")

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(shell)
	})
	mux.HandleFunc("/portada", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(portada)
	})
	mux.HandleFunc("/api/apps", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(scanApps())
	})
	// Obre l'Explorer del sistema amb el fitxer o carpeta seleccionats. Ho fan
	// servir les apps (p. ex. el "Localitza" de Guaita Fitxers) quan corren
	// dins del launcher, sense necessitat del pont Python extern.
	mux.HandleFunc("/api/reveal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "msg": "cal POST"})
			return
		}
		var cos struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&cos); err != nil || strings.TrimSpace(cos.Path) == "" {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "msg": "falta el camp path"})
			return
		}
		p := filepath.Clean(cos.Path)
		if _, err := os.Stat(p); err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "msg": "no s'ha trobat: " + p})
			return
		}
		switch runtime.GOOS {
		case "windows":
			exec.Command("explorer", "/select,", p).Start()
		case "darwin":
			exec.Command("open", "-R", p).Start()
		default:
			exec.Command("xdg-open", filepath.Dir(p)).Start()
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "msg": "Obrint l'Explorer…"})
	})
	// Serveix ./apps injectant el pont Guaita a cada HTML, perquè totes les
	// apps puguin rebre fitxers de la Safata sense modificar-les. La resta de
	// fitxers (imatges, etc.) se serveixen tal qual.
	bridge, _ := bridgeFS.ReadFile("web/guaita-bridge.js")
	injectat := []byte("\n<script>/* Guaita bridge (injectat pel launcher) */\n" + string(bridge) + "\n</script>\n")
	// Fons fosc del primer frame de pintat: evita el flaix blanc en carregar
	// l'app dins l'iframe. El CSS propi de cada app el sobreescriu de seguida.
	fonsInicial := []byte("<style>html{background:#121212}</style>")
	injectaFons := func(cos []byte) []byte {
		low := strings.ToLower(string(cos))
		i := strings.Index(low, "<head>")
		if i >= 0 {
			i += len("<head>")
		} else if i = strings.Index(low, "<html"); i >= 0 {
			if j := strings.Index(low[i:], ">"); j >= 0 {
				i += j + 1
			} else {
				i = 0
			}
		} else {
			i = 0
		}
		out := make([]byte, 0, len(cos)+len(fonsInicial))
		out = append(out, cos[:i]...)
		out = append(out, fonsInicial...)
		out = append(out, cos[i:]...)
		return out
	}
	mux.HandleFunc("/apps/", func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/apps/")
		rel = filepath.FromSlash(rel)
		full := filepath.Join(appsDir(), rel)
		// Guarda contra el path traversal: el resultat ha de quedar dins d'apps.
		if rp, err := filepath.Rel(appsDir(), full); err != nil || strings.HasPrefix(rp, "..") {
			http.NotFound(w, r)
			return
		}
		low := strings.ToLower(full)
		if !strings.HasSuffix(low, ".html") && !strings.HasSuffix(low, ".htm") {
			http.ServeFile(w, r, full)
			return
		}
		cos, err := os.ReadFile(full)
		if err != nil {
			// Resolució tolerant: potser el fitxer existeix amb un altre prefix
			// (número ordinal, separadors diferents). És el cas dels botons
			// d'ajuda de les apps, que enllacen "HLP Nom.html" mentre que a la
			// carpeta el fitxer es diu "110_HLP_Nom.html".
			if alt := resolTolerant(filepath.Base(rel)); alt != "" {
				cos, err = os.ReadFile(filepath.Join(appsDir(), alt))
			}
			if err != nil {
				// Últim recurs: una pàgina amable en lloc d'un "Not Found".
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(paginaAjudaNoTrobada))
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		cos = injectaFons(cos)
		// Injecta abans de </body> si hi és; si no, al final (els navegadors
		// executen igualment els scripts posteriors).
		if i := strings.LastIndex(strings.ToLower(string(cos)), "</body>"); i >= 0 {
			w.Write(cos[:i])
			w.Write(injectat)
			w.Write(cos[i:])
		} else {
			w.Write(cos)
			w.Write(injectat)
		}
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "no s'ha pogut obrir el port:", err)
		os.Exit(1)
	}
	url := fmt.Sprintf("http://%s/", ln.Addr().String())
	fmt.Println("Guaita Launcher a", url)

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	if os.Getenv("GUAITA_NOBROWSER") == "" {
		time.Sleep(200 * time.Millisecond)
		openBrowser(url)
	}

	// Manté el procés viu mentre serveix.
	select {}
}
