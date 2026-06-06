package toml

type Value any

type Array []Value

type Table map[string]Value

type Data struct {
	Sections     map[string]Table
	SectionOrder []string
	KeyOrder     map[string][]string
}

func NewData() Data {
	return Data{
		Sections:     make(map[string]Table),
		SectionOrder: []string{},
		KeyOrder:     make(map[string][]string),
	}
}

func (d Data) HasSection(name string) bool {
	_, ok := d.Sections[name]
	return ok
}

func (d Data) Section(name string) (Table, bool) {
	section, ok := d.Sections[name]
	return section, ok
}

func (d *Data) EnsureSection(name string) (Table, error) {
	if section, ok := d.Sections[name]; ok {
		return section, nil
	}
	if err := ensureSection(d, name); err != nil {
		return nil, err
	}
	return d.Sections[name], nil
}

func (d Data) Get(section, key string) (Value, bool) {
	table, ok := d.Sections[section]
	if !ok {
		return nil, false
	}
	value, ok := table[key]
	return value, ok
}

func Lookup[T any](d Data, section, key string) (T, bool, error) {
	value, ok := d.Get(section, key)
	if !ok {
		var zero T
		return zero, false, nil
	}
	decoded, err := As[T](value)
	if err != nil {
		var zero T
		return zero, true, err
	}
	return decoded, true, nil
}

func (t Table) Get(key string) (Value, bool) {
	value, ok := t[key]
	return value, ok
}

func LookupKey[T any](t Table, key string) (T, bool, error) {
	value, ok := t.Get(key)
	if !ok {
		var zero T
		return zero, false, nil
	}
	decoded, err := As[T](value)
	if err != nil {
		var zero T
		return zero, true, err
	}
	return decoded, true, nil
}
