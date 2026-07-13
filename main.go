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
// Ajuda: "HLP..." o bé "<num> HLP..." (p.ex. "130 HLP- Nom").
var helpPrefixRe = regexp.MustCompile(`(?i)^(\d+\s+)?hlp([\s_.-]|$)`)
// Neteja del prefix d'ajuda, amb el número ordinal opcional al davant.
var helpCleanRe = regexp.MustCompile(`(?i)^(\d+\s*[-_.]?\s*)?hlp\s*[-_.]?\s*`)

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
		h := appEntry{File: file, Nom: stripHelpPrefix(file), Mida: helpSize[file], Ajuda: true, Ordre: 999}
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
	// FileServer sobre ./apps evita el path traversal fora de l'arrel.
	mux.Handle("/apps/", http.StripPrefix("/apps/", http.FileServer(http.Dir(appsDir()))))

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
