package plugins

import (
	"archive/zip"
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"plugin"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/emqx/kuiper/common"
	"github.com/emqx/kuiper/common/kv"
	"github.com/emqx/kuiper/xstream/api"
)

type Plugin interface {
	GetName() string
	GetFile() string
	GetShellParas() []string
	GetSymbols() []string
	SetName(n string)
}

type IOPlugin struct {
	Name       string   `json:"name"`
	File       string   `json:"file"`
	ShellParas []string `json:"shellParas"`
}

func (p *IOPlugin) GetName() string {
	return p.Name
}

func (p *IOPlugin) GetFile() string {
	return p.File
}

func (p *IOPlugin) GetShellParas() []string {
	return p.ShellParas
}

func (p *IOPlugin) GetSymbols() []string {
	return nil
}

func (p *IOPlugin) SetName(n string) {
	p.Name = n
}

type FuncPlugin struct {
	IOPlugin
	// Optional, if not specified, a default element with the same name of the file will be registered
	Functions []string `json:"functions"`
}

func (fp *FuncPlugin) GetSymbols() []string {
	return fp.Functions
}

type PluginType int

func NewPluginByType(t PluginType) Plugin {
	switch t {
	case FUNCTION:
		return &FuncPlugin{}
	default:
		return &IOPlugin{}
	}
}

const (
	SOURCE PluginType = iota
	SINK
	FUNCTION
)

const DELETED = "$deleted"

var (
	PluginTypes = []string{"sources", "sinks", "functions"}
	once        sync.Once
	singleton   *Manager
)

//Registry is append only because plugin cannot delete or reload. To delete a plugin, restart the server to reindex
type Registry struct {
	sync.RWMutex
	// 3 maps for source/sink/function. In each map, key is the plugin name, value is the version
	plugins []map[string]string
	// A map from function name to its plugin file name. It is constructed during initialization by reading kv info. All functions must have at least an entry, even the function resizes in a one function plugin.
	symbols map[string]string
}

func (rr *Registry) Store(t PluginType, name string, version string) {
	rr.Lock()
	rr.plugins[t][name] = version
	rr.Unlock()
}

func (rr *Registry) StoreSymbols(name string, symbols []string) error {
	rr.Lock()
	defer rr.Unlock()
	for _, s := range symbols {
		if _, ok := rr.symbols[s]; ok {
			return fmt.Errorf("function name %s already exists", s)
		} else {
			rr.symbols[s] = name
		}
	}

	return nil
}

func (rr *Registry) RemoveSymbols(symbols []string) {
	rr.Lock()
	for _, s := range symbols {
		delete(rr.symbols, s)
	}
	rr.Unlock()
}

func (rr *Registry) List(t PluginType) []string {
	rr.RLock()
	result := rr.plugins[t]
	rr.RUnlock()
	keys := make([]string, 0, len(result))
	for k := range result {
		keys = append(keys, k)
	}
	return keys
}

func (rr *Registry) ListSymbols() []string {
	rr.RLock()
	result := rr.symbols
	rr.RUnlock()
	keys := make([]string, 0, len(result))
	for k := range result {
		keys = append(keys, k)
	}
	return keys
}

func (rr *Registry) Get(t PluginType, name string) (string, bool) {
	rr.RLock()
	result := rr.plugins[t]
	rr.RUnlock()
	r, ok := result[name]
	return r, ok
}

func (rr *Registry) GetPluginVersionBySymbol(t PluginType, symbolName string) (string, bool) {
	switch t {
	case FUNCTION:
		rr.RLock()
		result := rr.plugins[t]
		name, ok := rr.symbols[symbolName]
		rr.RUnlock()
		if ok {
			r, nok := result[name]
			return r, nok
		} else {
			return "", false
		}
	default:
		return rr.Get(t, symbolName)
	}
}

func (rr *Registry) GetPluginBySymbol(t PluginType, symbolName string) (string, bool) {
	switch t {
	case FUNCTION:
		rr.RLock()
		defer rr.RUnlock()
		name, ok := rr.symbols[symbolName]
		return name, ok
	default:
		return symbolName, true
	}
}

var symbolRegistry = make(map[string]plugin.Symbol)
var mu sync.RWMutex

func getPlugin(t string, pt PluginType) (plugin.Symbol, error) {
	ut := ucFirst(t)
	ptype := PluginTypes[pt]
	key := ptype + "/" + t
	mu.Lock()
	defer mu.Unlock()
	var nf plugin.Symbol
	nf, ok := symbolRegistry[key]
	if !ok {
		m, err := NewPluginManager()
		if err != nil {
			return nil, fmt.Errorf("fail to initialize the plugin manager")
		}
		mod, err := getSoFilePath(m, pt, t, false)
		if err != nil {
			return nil, fmt.Errorf("cannot get the plugin file path: %v", err)
		}
		common.Log.Debugf("Opening plugin %s", mod)
		plug, err := plugin.Open(mod)
		if err != nil {
			return nil, fmt.Errorf("cannot open %s: %v", mod, err)
		}
		common.Log.Debugf("Successfully open plugin %s", mod)
		nf, err = plug.Lookup(ut)
		if err != nil {
			return nil, fmt.Errorf("cannot find symbol %s, please check if it is exported", t)
		}
		common.Log.Debugf("Successfully look-up plugin %s", mod)
		symbolRegistry[key] = nf
	}
	return nf, nil
}

func GetSource(t string) (api.Source, error) {
	// from local map
	if p, ok := getSourceFromNative(t); ok {
		return p, nil
	}

	nf, err := getPlugin(t, SOURCE)
	if err != nil {
		return nil, err
	}
	var s api.Source
	switch t := nf.(type) {
	case api.Source:
		s = t
	case func() api.Source:
		s = t()
	default:
		return nil, fmt.Errorf("exported symbol %s is not type of api.Source or function that return api.Source", t)
	}
	return s, nil
}

func GetSink(t string) (api.Sink, error) {
	// from local map
	if p, ok := getSinkFromNative(t); ok {
		return p, nil
	}

	nf, err := getPlugin(t, SINK)
	if err != nil {
		return nil, err
	}
	var s api.Sink
	switch t := nf.(type) {
	case api.Sink:
		s = t
	case func() api.Sink:
		s = t()
	default:
		return nil, fmt.Errorf("exported symbol %s is not type of api.Sink or function that return api.Sink", t)
	}
	return s, nil
}

func GetFunction(t string) (api.Function, error) {
	// from local map
	if p, ok := getFunctionsFromNative(t); ok {
		return p, nil
	}

	nf, err := getPlugin(t, FUNCTION)
	if err != nil {
		return nil, err
	}
	var s api.Function
	switch t := nf.(type) {
	case api.Function:
		s = t
	case func() api.Function:
		s = t()
	default:
		return nil, fmt.Errorf("exported symbol %s is not type of api.Function or function that return api.Function", t)
	}
	return s, nil
}

type Manager struct {
	pluginDir string
	etcDir    string
	registry  *Registry
	db        kv.KeyValue
}

func NewPluginManager() (*Manager, error) {
	var outerErr error
	once.Do(func() {
		dir, err := common.GetLoc("/plugins")
		if err != nil {
			outerErr = fmt.Errorf("cannot find plugins folder: %s", err)
			return
		}
		etcDir, err := common.GetLoc("/etc")
		if err != nil {
			outerErr = fmt.Errorf("cannot find etc folder: %s", err)
			return
		}
		dbDir, err := common.GetDataLoc()
		if err != nil {
			outerErr = fmt.Errorf("cannot find db folder: %s", err)
			return
		}
		db := kv.GetDefaultKVStore(path.Join(dbDir, "pluginFuncs"))
		err = db.Open()
		if err != nil {
			outerErr = fmt.Errorf("error when opening db: %v.", err)
		}
		defer db.Close()
		plugins := make([]map[string]string, 3)
		for i := 0; i < 3; i++ {
			names, err := findAll(PluginType(i), dir)
			if err != nil {
				outerErr = fmt.Errorf("fail to find existing plugins: %s", err)
				return
			}
			plugins[i] = names
		}
		registry := &Registry{plugins: plugins, symbols: make(map[string]string)}
		for pf, _ := range plugins[FUNCTION] {
			l := make([]string, 0)
			if ok, err := db.Get(pf, &l); ok {
				registry.StoreSymbols(pf, l)
			} else if err != nil {
				outerErr = fmt.Errorf("error when querying kv: %s", err)
				return
			} else {
				registry.StoreSymbols(pf, []string{pf})
			}
		}

		singleton = &Manager{
			pluginDir: dir,
			etcDir:    etcDir,
			registry:  registry,
			db:        db,
		}
		if err := singleton.readSourceMetaDir(); nil != err {
			common.Log.Errorf("readSourceMetaDir:%v", err)
		}
		if err := singleton.readSinkMetaDir(); nil != err {
			common.Log.Errorf("readSinkMetaDir:%v", err)
		}
		if err := singleton.readFuncMetaDir(); nil != err {
			common.Log.Errorf("readFuncMetaDir:%v", err)
		}
		if err := singleton.readUiMsgDir(); nil != err {
			common.Log.Errorf("readUiMsgDir:%v", err)
		}
	})
	return singleton, outerErr
}

func findAll(t PluginType, pluginDir string) (result map[string]string, err error) {
	result = make(map[string]string)
	dir := path.Join(pluginDir, PluginTypes[t])
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return
	}

	for _, file := range files {
		baseName := filepath.Base(file.Name())
		if strings.HasSuffix(baseName, ".so") {
			n, v := parseName(baseName)
			result[n] = v
		}
	}
	return
}

func (m *Manager) List(t PluginType) (result []string, err error) {
	return m.registry.List(t), nil
}

func (m *Manager) ListSymbols() (result []string, err error) {
	return m.registry.ListSymbols(), nil
}

func (m *Manager) GetSymbol(s string) (result string, ok bool) {
	return m.registry.GetPluginBySymbol(FUNCTION, s)
}

func (m *Manager) Register(t PluginType, j Plugin) error {
	name, uri, shellParas := j.GetName(), j.GetFile(), j.GetShellParas()
	//Validation
	name = strings.Trim(name, " ")
	if name == "" {
		return fmt.Errorf("invalid name %s: should not be empty", name)
	}
	if !isValidUrl(uri) || !strings.HasSuffix(uri, ".zip") {
		return fmt.Errorf("invalid uri %s", uri)
	}

	if v, ok := m.registry.Get(t, name); ok {
		if v == DELETED {
			return fmt.Errorf("invalid name %s: the plugin is marked as deleted but Kuiper is not restarted for the change to take effect yet", name)
		} else {
			return fmt.Errorf("invalid name %s: duplicate", name)
		}
	}
	var err error
	if t == FUNCTION {
		if len(j.GetSymbols()) > 0 {
			err = m.db.Open()
			if err != nil {
				return err
			}
			err = m.db.Set(name, j.GetSymbols())
			if err != nil {
				return err
			}
			m.db.Close()
			err = m.registry.StoreSymbols(name, j.GetSymbols())
		} else {
			err = m.registry.StoreSymbols(name, []string{name})
		}
	}
	if err != nil {
		return err
	}

	zipPath := path.Join(m.pluginDir, name+".zip")
	var unzipFiles []string
	//clean up: delete zip file and unzip files in error
	defer os.Remove(zipPath)
	//download
	err = downloadFile(zipPath, uri)
	if err != nil {
		return fmt.Errorf("fail to download file %s: %s", uri, err)
	}
	//unzip and copy to destination
	unzipFiles, version, err := m.install(t, name, zipPath, shellParas)
	if err == nil && len(j.GetSymbols()) > 0 {
		if err = m.db.Open(); err == nil {
			err = m.db.Set(name, j.GetSymbols())
		}
	}
	if err != nil { //Revert for any errors
		if t == SOURCE && len(unzipFiles) == 1 { //source that only copy so file
			os.RemoveAll(unzipFiles[0])
		}
		if len(j.GetSymbols()) > 0 {
			m.db.Close()
			m.registry.RemoveSymbols(j.GetSymbols())
		} else {
			m.registry.RemoveSymbols([]string{name})
		}
		return fmt.Errorf("fail to install plugin: %s", err)
	}
	m.registry.Store(t, name, version)

	switch t {
	case SINK:
		if err := m.readSinkMetaFile(path.Join(m.etcDir, PluginTypes[t], name+`.json`)); nil != err {
			common.Log.Errorf("readSinkFile:%v", err)
		}
	case SOURCE:
		if err := m.readSourceMetaFile(path.Join(m.etcDir, PluginTypes[t], name+`.json`)); nil != err {
			common.Log.Errorf("readSourceFile:%v", err)
		}
	case FUNCTION:
		if err := m.readFuncMetaFile(path.Join(m.etcDir, PluginTypes[t], name+`.json`)); nil != err {
			common.Log.Errorf("readFuncFile:%v", err)
		}
	}
	return nil
}

// prerequisite：function plugin of name exists
func (m *Manager) RegisterFuncs(name string, functions []string) error {
	if len(functions) == 0 {
		return fmt.Errorf("property 'functions' must not be empty")
	}
	err := m.db.Open()
	if err != nil {
		return err
	}
	defer m.db.Close()
	old := make([]string, 0)
	if ok, err := m.db.Get(name, &old); err != nil {
		return err
	} else if ok {
		m.registry.RemoveSymbols(old)
	} else if !ok {
		m.registry.RemoveSymbols([]string{name})
	}
	err = m.db.Set(name, functions)
	if err != nil {
		return err
	}
	return m.registry.StoreSymbols(name, functions)
}

func (m *Manager) Delete(t PluginType, name string, stop bool) error {
	name = strings.Trim(name, " ")
	if name == "" {
		return fmt.Errorf("invalid name %s: should not be empty", name)
	}
	soPath, err := getSoFilePath(m, t, name, true)
	if err != nil {
		return err
	}
	var results []string
	paths := []string{
		soPath,
	}
	// Find etc folder
	etcPath := path.Join(m.etcDir, PluginTypes[t], name)
	if fi, err := os.Stat(etcPath); err == nil {
		if fi.Mode().IsDir() {
			paths = append(paths, etcPath)
		}
	}
	switch t {
	case SOURCE:
		paths = append(paths, path.Join(m.etcDir, PluginTypes[t], name+".yaml"))
		m.uninstalSource(name)
	case SINK:
		m.uninstalSink(name)
	case FUNCTION:
		old := make([]string, 0)
		err = m.db.Open()
		if err != nil {
			return err
		}
		if ok, err := m.db.Get(name, &old); err != nil {
			return err
		} else if ok {
			m.registry.RemoveSymbols(old)
			err := m.db.Delete(name)
			if err != nil {
				return err
			}
		} else if !ok {
			m.registry.RemoveSymbols([]string{name})
		}
		m.db.Close()
		m.uninstalFunc(name)
	}

	for _, p := range paths {
		_, err := os.Stat(p)
		if err == nil {
			err = os.RemoveAll(p)
			if err != nil {
				results = append(results, err.Error())
			}
		} else {
			results = append(results, fmt.Sprintf("can't find %s", p))
		}
	}

	if len(results) > 0 {
		return errors.New(strings.Join(results, "\n"))
	} else {
		m.registry.Store(t, name, DELETED)
		if stop {
			go func() {
				time.Sleep(1 * time.Second)
				os.Exit(100)
			}()
		}
		return nil
	}
}
func (m *Manager) Get(t PluginType, name string) (map[string]interface{}, bool) {
	v, ok := m.registry.Get(t, name)
	if strings.HasPrefix(v, "v") {
		v = v[1:]
	}
	if ok {
		r := map[string]interface{}{
			"name":    name,
			"version": v,
		}
		if t == FUNCTION {
			if err := m.db.Open(); err == nil {
				l := make([]string, 0)
				if ok, _ := m.db.Get(name, &l); ok {
					r["functions"] = l
				}
				m.db.Close()
			}
			// ignore the error
		}
		return r, ok
	}
	return nil, false
}

// Return the lowercase version of so name. It may be upper case in path.
func getSoFilePath(m *Manager, t PluginType, name string, isSoName bool) (string, error) {
	var (
		v      string
		soname string
		ok     bool
	)
	// We must identify plugin or symbol when deleting function plugin
	if isSoName {
		soname = name
	} else {
		soname, ok = m.registry.GetPluginBySymbol(t, name)
		if !ok {
			return "", common.NewErrorWithCode(common.NOT_FOUND, fmt.Sprintf("invalid symbol name %s: not exist", name))
		}
	}
	v, ok = m.registry.Get(t, soname)
	if !ok {
		return "", common.NewErrorWithCode(common.NOT_FOUND, fmt.Sprintf("invalid name %s: not exist", soname))
	}

	soFile := soname + ".so"
	if v != "" {
		soFile = fmt.Sprintf("%s@%s.so", soname, v)
	}
	p := path.Join(m.pluginDir, PluginTypes[t], soFile)
	if _, err := os.Stat(p); err != nil {
		p = path.Join(m.pluginDir, PluginTypes[t], ucFirst(soFile))
	}
	if _, err := os.Stat(p); err != nil {
		return "", common.NewErrorWithCode(common.NOT_FOUND, fmt.Sprintf("cannot find .so file for plugin %s", soname))
	}
	return p, nil
}

func (m *Manager) install(t PluginType, name, src string, shellParas []string) ([]string, string, error) {
	var filenames []string
	var tempPath = path.Join(m.pluginDir, "temp", PluginTypes[t], name)
	defer os.RemoveAll(tempPath)
	r, err := zip.OpenReader(src)
	if err != nil {
		return filenames, "", err
	}
	defer r.Close()

	soPrefix := regexp.MustCompile(fmt.Sprintf(`^((%s)|(%s))(@.*)?\.so$`, name, ucFirst(name)))
	var yamlFile, yamlPath, version string
	expFiles := 1
	if t == SOURCE {
		yamlFile = name + ".yaml"
		yamlPath = path.Join(m.etcDir, PluginTypes[t], yamlFile)
		expFiles = 2
	}
	var revokeFiles []string
	needInstall := false
	for _, file := range r.File {
		fileName := file.Name
		if yamlFile == fileName {
			err = unzipTo(file, yamlPath)
			if err != nil {
				return filenames, "", err
			}
			revokeFiles = append(revokeFiles, yamlPath)
			filenames = append(filenames, yamlPath)
		} else if fileName == name+".json" {
			jsonPath := path.Join(m.etcDir, PluginTypes[t], fileName)
			if err := unzipTo(file, jsonPath); nil != err {
				common.Log.Errorf("Failed to decompress the metadata %s file", fileName)
			} else {
				revokeFiles = append(revokeFiles, jsonPath)
			}
		} else if soPrefix.Match([]byte(fileName)) {
			soPath := path.Join(m.pluginDir, PluginTypes[t], fileName)
			err = unzipTo(file, soPath)
			if err != nil {
				return filenames, "", err
			}
			filenames = append(filenames, soPath)
			revokeFiles = append(revokeFiles, soPath)
			_, version = parseName(fileName)
		} else if strings.HasPrefix(fileName, "etc/") {
			err = unzipTo(file, path.Join(m.etcDir, PluginTypes[t], strings.Replace(fileName, "etc", name, 1)))
			if err != nil {
				return filenames, "", err
			}
		} else { //unzip other files
			err = unzipTo(file, path.Join(tempPath, fileName))
			if err != nil {
				return filenames, "", err
			}
			if fileName == "install.sh" {
				needInstall = true
			}
		}
	}
	if len(filenames) != expFiles {
		return filenames, version, fmt.Errorf("invalid zip file: so file or conf file is missing")
	} else if needInstall {
		//run install script if there is
		spath := path.Join(tempPath, "install.sh")
		shellParas = append(shellParas, spath)
		if 1 != len(shellParas) {
			copy(shellParas[1:], shellParas[0:])
			shellParas[0] = spath
		}
		cmd := exec.Command("/bin/sh", shellParas...)
		var outb, errb bytes.Buffer
		cmd.Stdout = &outb
		cmd.Stderr = &errb
		err := cmd.Run()

		if err != nil {
			for _, f := range revokeFiles {
				os.RemoveAll(f)
			}
			common.Log.Infof(`err:%v stdout:%s stderr:%s`, err, outb.String(), errb.String())
			return filenames, "", err
		} else {
			common.Log.Infof(`run install script:%s`, outb.String())
			common.Log.Infof("install %s plugin %s", PluginTypes[t], name)
		}
	}
	return filenames, version, nil
}

func parseName(n string) (string, string) {
	result := strings.Split(n, ".so")
	result = strings.Split(result[0], "@")
	name := lcFirst(result[0])
	if len(result) > 1 {
		return name, result[1]
	}
	return name, ""
}

func unzipTo(f *zip.File, fpath string) error {
	_, err := os.Stat(fpath)
	if err == nil || !os.IsNotExist(err) {
		if err = os.RemoveAll(fpath); err != nil {
			return fmt.Errorf("failed to delete file %s", fpath)
		}
	}

	if f.FileInfo().IsDir() {
		// Make Folder
		os.MkdirAll(fpath, os.ModePerm)
		return nil
	}

	if _, err := os.Stat(filepath.Dir(fpath)); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}
	}

	outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}

	rc, err := f.Open()
	if err != nil {
		return err
	}

	_, err = io.Copy(outFile, rc)

	outFile.Close()
	rc.Close()
	return err
}

func isValidUrl(uri string) bool {
	pu, err := url.ParseRequestURI(uri)
	if err != nil {
		return false
	}

	switch pu.Scheme {
	case "http", "https":
		u, err := url.Parse(uri)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return false
		}
	case "file":
		if pu.Host != "" || pu.Path == "" {
			return false
		}
	default:
		return false
	}
	return true
}

func downloadFile(filepath string, uri string) error {
	common.Log.Infof("Start to download file %s\n", uri)
	u, err := url.ParseRequestURI(uri)
	if err != nil {
		return err
	}
	var src io.Reader
	switch u.Scheme {
	case "file":
		// deal with windows path
		if strings.Index(u.Path, ":") == 2 {
			u.Path = u.Path[1:]
		}
		common.Log.Debugf(u.Path)
		sourceFileStat, err := os.Stat(u.Path)
		if err != nil {
			return err
		}

		if !sourceFileStat.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", u.Path)
		}
		srcFile, err := os.Open(u.Path)
		if err != nil {
			return err
		}
		defer srcFile.Close()
		src = srcFile
	case "http", "https":
		// Get the data
		timeout := time.Duration(5 * time.Minute)
		client := &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
		resp, err := client.Get(uri)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("cannot download the file with status: %s", resp.Status)
		}
		defer resp.Body.Close()
		src = resp.Body
	default:
		return fmt.Errorf("unsupported url scheme %s", u.Scheme)
	}
	// Create the file
	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, src)
	return err
}

func ucFirst(str string) string {
	for i, v := range str {
		return string(unicode.ToUpper(v)) + str[i+1:]
	}
	return ""
}

func lcFirst(str string) string {
	for i, v := range str {
		return string(unicode.ToLower(v)) + str[i+1:]
	}
	return ""
}
