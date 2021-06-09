package plugins

import (
	"fmt"
	"github.com/emqx/kuiper/common"
	"github.com/emqx/kuiper/xstream/api"
	"io/ioutil"
	"path"
	"reflect"
	"strings"
)

const (
	baseProperty = `properties`
	baseOption   = `options`
	sink         = `sink`
	source       = `source`
)

type (
	author struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Company string `json:"company"`
		Website string `json:"website"`
	}
	fileLanguage struct {
		English string `json:"en_US"`
		Chinese string `json:"zh_CN"`
	}
	fileField struct {
		Name     string        `json:"name"`
		Default  interface{}   `json:"default"`
		Control  string        `json:"control"`
		Optional bool          `json:"optional"`
		Type     string        `json:"type"`
		Hint     *fileLanguage `json:"hint"`
		Label    *fileLanguage `json:"label"`
		Values   interface{}   `json:"values"`
	}
	fileAbout struct {
		Trial       bool          `json:"trial"`
		Installed   bool          `json:"installed"`
		Author      *author       `json:"author"`
		HelpUrl     *fileLanguage `json:"helpUrl"`
		Description *fileLanguage `json:"description"`
	}
	fileSink struct {
		About  *fileAbout   `json:"about"`
		Libs   []string     `json:"libs"`
		Fields []*fileField `json:"properties"`
	}
	language struct {
		English string `json:"en"`
		Chinese string `json:"zh"`
	}
	about struct {
		Trial       bool      `json:"trial"`
		Installed   bool      `json:"installed"`
		Author      *author   `json:"author"`
		HelpUrl     *language `json:"helpUrl"`
		Description *language `json:"description"`
	}
	field struct {
		Exist    bool        `json:"exist"`
		Name     string      `json:"name"`
		Default  interface{} `json:"default"`
		Type     string      `json:"type"`
		Control  string      `json:"control"`
		Optional bool        `json:"optional"`
		Values   interface{} `json:"values"`
		Hint     *language   `json:"hint"`
		Label    *language   `json:"label"`
	}

	uiSink struct {
		About  *about   `json:"about"`
		Libs   []string `json:"libs"`
		Fields []field  `json:"properties"` // mutable, so each sink must have its own copy
	}
	uiSinks struct {
		CustomProperty map[string]uiSink `json:"customProperty"`
		BaseProperty   map[string]uiSink `json:"baseProperty"`
		BaseOption     uiSink            `json:"baseOption"`
		language       string
	}
)

func isInternalSink(fiName string) bool {
	internal := []string{`edgex.json`, `log.json`, `mqtt.json`, `nop.json`, `rest.json`}
	for _, v := range internal {
		if v == fiName {
			return true
		}
	}
	return false
}
func newLanguage(fi *fileLanguage) *language {
	if nil == fi {
		return nil
	}
	ui := new(language)
	ui.English = fi.English
	ui.Chinese = fi.Chinese
	return ui
}
func newField(fis []*fileField) (uis []field, err error) {
	for _, fi := range fis {
		if nil == fi {
			continue
		}
		ui := field{
			Name:     fi.Name,
			Type:     fi.Type,
			Control:  fi.Control,
			Optional: fi.Optional,
			Values:   fi.Values,
			Hint:     newLanguage(fi.Hint),
			Label:    newLanguage(fi.Label),
		}
		uis = append(uis, ui)
		switch t := fi.Default.(type) {
		case []map[string]interface{}:
			var auxFi []*fileField
			if err = common.MapToStruct(t, &auxFi); nil != err {
				return nil, err
			}
			if ui.Default, err = newField(auxFi); nil != err {
				return nil, err
			}
		default:
			ui.Default = fi.Default
		}
	}
	return uis, err
}
func newAbout(fi *fileAbout) *about {
	if nil == fi {
		return nil
	}
	ui := new(about)
	ui.Trial = fi.Trial
	ui.Installed = fi.Installed
	ui.Author = fi.Author
	ui.HelpUrl = newLanguage(fi.HelpUrl)
	ui.Description = newLanguage(fi.Description)
	return ui
}
func newUiSink(fi *fileSink) (*uiSink, error) {
	if nil == fi {
		return nil, nil
	}
	var err error
	ui := new(uiSink)
	ui.Libs = fi.Libs
	ui.About = newAbout(fi.About)
	ui.Fields, err = newField(fi.Fields)
	return ui, err
}

var g_sinkMetadata map[string]*uiSink //map[fileName]
func (m *Manager) readSinkMetaDir() error {
	g_sinkMetadata = make(map[string]*uiSink)
	confDir, err := common.GetConfLoc()
	if nil != err {
		return err
	}

	dir := path.Join(confDir, "sinks")
	files, err := ioutil.ReadDir(dir)
	if nil != err {
		return err
	}
	for _, file := range files {
		fname := file.Name()
		if !strings.HasSuffix(fname, ".json") {
			continue
		}

		filePath := path.Join(dir, fname)
		if err := m.readSinkMetaFile(filePath); nil != err {
			return err
		}
	}
	return nil
}

func (m *Manager) uninstalSink(name string) {
	if ui, ok := g_sinkMetadata[name+".json"]; ok {
		if nil != ui.About {
			ui.About.Installed = false
		}
	}
}
func (m *Manager) readSinkMetaFile(filePath string) error {
	finame := path.Base(filePath)
	pluginName := strings.TrimSuffix(finame, `.json`)
	metadata := new(fileSink)
	err := common.ReadJsonUnmarshal(filePath, metadata)
	if nil != err {
		return fmt.Errorf("filePath:%s err:%v", filePath, err)
	}
	if pluginName != baseProperty && pluginName != baseOption {
		if nil == metadata.About {
			return fmt.Errorf("not found about of %s", finame)
		} else if isInternalSink(finame) {
			metadata.About.Installed = true
		} else {
			_, metadata.About.Installed = m.registry.Get(SINK, pluginName)
		}
	}
	g_sinkMetadata[finame], err = newUiSink(metadata)
	if nil != err {
		return err
	}
	common.Log.Infof("Loading metadata file for sink: %s", finame)
	return nil
}

func (us *uiSinks) setCustomProperty(pluginName string) error {
	fileName := pluginName + `.json`
	sinkMetadata := g_sinkMetadata
	data, ok := sinkMetadata[fileName]
	if !ok {
		return fmt.Errorf(`%s%s`, getMsg(us.language, sink, "not_found_plugin"), pluginName)
	}
	if 0 == len(us.CustomProperty) {
		us.CustomProperty = make(map[string]uiSink)
	}
	us.CustomProperty[pluginName] = data.clone()
	return nil
}

func (us *uiSinks) setBasePropertry(pluginName string) error {
	sinkMetadata := g_sinkMetadata
	data := sinkMetadata[baseProperty+".json"]
	if nil == data {
		return fmt.Errorf(`%s%s`, getMsg(us.language, sink, "not_found_plugin"), baseProperty)
	}
	if 0 == len(us.BaseProperty) {
		us.BaseProperty = make(map[string]uiSink)
	}
	us.BaseProperty[pluginName] = data.clone()
	return nil
}

func (us *uiSinks) setBaseOption() error {
	sinkMetadata := g_sinkMetadata
	data := sinkMetadata[baseOption+".json"]
	if nil == data {
		return fmt.Errorf(`%s%s`, getMsg(us.language, sink, "not_found_plugin"), baseOption)
	}
	us.BaseOption = data.clone()
	return nil
}

func (us *uiSinks) hintWhenNewSink(pluginName string) (err error) {
	err = us.setCustomProperty(pluginName)
	if nil != err {
		return err
	}
	err = us.setBasePropertry(pluginName)
	if nil != err {
		return err
	}
	err = us.setBaseOption()
	return err
}

func (us *uiSinks) modifyCustom(uiFields []field, ruleFields map[string]interface{}) (err error) {
	for i, ui := range uiFields {
		ruleVal := ruleFields[ui.Name]
		if nil == ruleVal {
			continue
		}
		if reflect.Map == reflect.TypeOf(ruleVal).Kind() && "object" != ui.Type {
			var auxRuleFields map[string]interface{}
			if err := common.MapToStruct(ruleVal, &auxRuleFields); nil != err {
				return fmt.Errorf(`%s%v %s`, getMsg(us.language, sink, "type_conversion_fail"), err, ui.Name)
			}
			var auxUiFields []field
			if err := common.MapToStruct(ui.Default, &auxUiFields); nil != err {
				return fmt.Errorf(`%s%v %s`, getMsg(us.language, sink, "type_conversion_fail"), err, ui.Name)
			}
			uiFields[i].Default = auxUiFields
			if err := us.modifyCustom(auxUiFields, auxRuleFields); nil != err {
				return err
			}
		} else {
			uiFields[i].Default = ruleVal
		}
	}
	return nil
}

func (u *uiSink) clone() (c uiSink) {
	c.About = u.About
	c.Libs = u.Libs
	c.Fields = make([]field, len(u.Fields))
	for i, f := range u.Fields {
		c.Fields[i] = f
	}
	return
}

func (u *uiSink) modifyBase(mapFields map[string]interface{}) {
	for i, field := range u.Fields {
		fieldVal := mapFields[field.Name]
		if nil != fieldVal {
			u.Fields[i].Default = fieldVal
		}
	}
}

func (us *uiSinks) modifyProperty(pluginName string, mapFields map[string]interface{}) (err error) {
	custom, ok := us.CustomProperty[pluginName]
	if !ok {
		return fmt.Errorf(`%s%s`, getMsg(us.language, sink, "not_found_plugin"), pluginName)
	}
	if err = us.modifyCustom(custom.Fields, mapFields); nil != err {
		return err
	}

	base, ok := us.BaseProperty[pluginName]
	if !ok {
		return fmt.Errorf(`%s%s`, getMsg(us.language, sink, "not_found_plugin"), pluginName)
	}
	base.modifyBase(mapFields)
	return nil
}

func (us *uiSinks) modifyOption(option *api.RuleOption) {
	baseOption := us.BaseOption
	for i, field := range baseOption.Fields {
		switch field.Name {
		case `isEventTime`:
			baseOption.Fields[i].Default = option.IsEventTime
		case `lateTol`:
			baseOption.Fields[i].Default = option.LateTol
		case `concurrency`:
			baseOption.Fields[i].Default = option.Concurrency
		case `bufferLength`:
			baseOption.Fields[i].Default = option.BufferLength
		case `sendMetaToSink`:
			baseOption.Fields[i].Default = option.SendMetaToSink
		case `qos`:
			baseOption.Fields[i].Default = option.Qos
		case `checkpointInterval`:
			baseOption.Fields[i].Default = option.CheckpointInterval
		}
	}
}

func (us *uiSinks) hintWhenModifySink(rule *api.Rule) (err error) {
	for _, m := range rule.Actions {
		for pluginName, sink := range m {
			mapFields, _ := sink.(map[string]interface{})
			err = us.hintWhenNewSink(pluginName)
			if nil != err {
				return err
			}
			if err := us.modifyProperty(pluginName, mapFields); nil != err {
				return err
			}
		}
	}
	us.modifyOption(rule.Options)
	return nil
}

func GetSinkMeta(pluginName, language string, rule *api.Rule) (ptrSinkProperty *uiSinks, err error) {
	ptrSinkProperty = new(uiSinks)
	ptrSinkProperty.language = language
	if nil == rule {
		err = ptrSinkProperty.hintWhenNewSink(pluginName)
	} else {
		err = ptrSinkProperty.hintWhenModifySink(rule)
	}
	return ptrSinkProperty, err
}

type pluginfo struct {
	Name  string `json:"name"`
	About *about `json:"about"`
}

func GetSinks() (sinks []*pluginfo) {
	sinkMeta := g_sinkMetadata
	for fileName, v := range sinkMeta {
		if fileName == baseProperty+".json" || fileName == baseOption+".json" {
			continue
		}
		node := new(pluginfo)
		node.Name = strings.TrimSuffix(fileName, `.json`)
		node.About = v.About
		i := 0
		for ; i < len(sinks); i++ {
			if node.Name <= sinks[i].Name {
				sinks = append(sinks, node)
				copy(sinks[i+1:], sinks[i:])
				sinks[i] = node
				break
			}
		}
		if len(sinks) == i {
			sinks = append(sinks, node)
		}
	}
	return sinks
}
