package toml

import (
	"fmt"
	"reflect"
	"strings"
)

type fieldOptions struct {
	name       string
	ignore     bool
	inline     bool
	defaultTop bool
}

func As[T any](value Value) (T, error) {
	var out T
	target := reflect.ValueOf(&out).Elem()
	if err := assignValue(target, value); err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

func (d Data) Decode(target any) error {
	rv := reflect.ValueOf(target)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("target must be a non-nil pointer")
	}
	if rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("target must point to a struct")
	}
	return decodeDataIntoStruct(rv.Elem(), d)
}

func decodeDataIntoStruct(target reflect.Value, data Data) error {
	targetType := target.Type()
	for i := range targetType.NumField() {
		field := targetType.Field(i)
		if !field.IsExported() {
			continue
		}
		opts := parseFieldOptions(field)
		if opts.ignore {
			continue
		}

		dest := target.Field(i)
		if opts.defaultTop {
			if section, ok := data.Sections["default"]; ok {
				if err := assignValue(dest, section); err != nil {
					return fmt.Errorf("decode default into field %q: %w", field.Name, err)
				}
			}
			continue
		}

		if opts.inline {
			if err := assignValue(dest, data.Sections); err != nil {
				return fmt.Errorf("decode inline field %q: %w", field.Name, err)
			}
			continue
		}

		sectionName := opts.name
		if sectionName == "" {
			sectionName = strings.ToLower(field.Name)
		}
		section, ok := data.Sections[sectionName]
		if !ok {
			continue
		}
		if err := assignValue(dest, section); err != nil {
			return fmt.Errorf("decode section %q into field %q: %w", sectionName, field.Name, err)
		}
	}
	return nil
}

func assignValue(target reflect.Value, raw any) error {
	if !target.CanSet() {
		return fmt.Errorf("target cannot be set")
	}
	if raw == nil {
		target.Set(reflect.Zero(target.Type()))
		return nil
	}

	if target.Kind() == reflect.Pointer {
		if target.IsNil() {
			target.Set(reflect.New(target.Type().Elem()))
		}
		return assignValue(target.Elem(), raw)
	}

	source := reflect.ValueOf(raw)
	if source.IsValid() {
		if source.Type().AssignableTo(target.Type()) {
			target.Set(source)
			return nil
		}
		if source.Type().ConvertibleTo(target.Type()) && isScalarKind(source.Kind()) && isScalarKind(target.Kind()) {
			target.Set(source.Convert(target.Type()))
			return nil
		}
	}

	switch target.Kind() {
	case reflect.Struct:
		table, ok := raw.(Table)
		if !ok {
			return fmt.Errorf("cannot decode %T into struct %s", raw, target.Type())
		}
		return decodeTableIntoStruct(target, table)
	case reflect.Map:
		return assignMap(target, raw)
	case reflect.Slice:
		return assignSlice(target, raw)
	case reflect.Interface:
		target.Set(source)
		return nil
	default:
		return fmt.Errorf("cannot decode %T into %s", raw, target.Type())
	}
}

func decodeTableIntoStruct(target reflect.Value, table Table) error {
	targetType := target.Type()
	for i := range targetType.NumField() {
		field := targetType.Field(i)
		if !field.IsExported() {
			continue
		}
		opts := parseFieldOptions(field)
		if opts.ignore {
			continue
		}

		fieldValue := target.Field(i)
		if opts.inline {
			if err := assignValue(fieldValue, table); err != nil {
				return fmt.Errorf("inline field %q: %w", field.Name, err)
			}
			continue
		}

		key := opts.name
		if key == "" {
			key = strings.ToLower(field.Name)
		}
		raw, ok := table[key]
		if !ok {
			continue
		}
		if err := assignValue(fieldValue, raw); err != nil {
			return fmt.Errorf("field %q: %w", field.Name, err)
		}
	}
	return nil
}

func assignMap(target reflect.Value, raw any) error {
	input := reflect.ValueOf(raw)
	if input.Kind() != reflect.Map {
		return fmt.Errorf("cannot decode %T into map %s", raw, target.Type())
	}
	if target.IsNil() {
		target.Set(reflect.MakeMapWithSize(target.Type(), input.Len()))
	}
	iter := input.MapRange()
	for iter.Next() {
		key := reflect.New(target.Type().Key()).Elem()
		if err := assignValue(key, iter.Key().Interface()); err != nil {
			return fmt.Errorf("map key: %w", err)
		}
		value := reflect.New(target.Type().Elem()).Elem()
		if err := assignValue(value, iter.Value().Interface()); err != nil {
			return fmt.Errorf("map value for %v: %w", iter.Key().Interface(), err)
		}
		target.SetMapIndex(key, value)
	}
	return nil
}

func assignSlice(target reflect.Value, raw any) error {
	values, ok := raw.([]Value)
	if !ok {
		array, isArray := raw.(Array)
		if !isArray {
			return fmt.Errorf("cannot decode %T into slice %s", raw, target.Type())
		}
		values = []Value(array)
	}
	result := reflect.MakeSlice(target.Type(), len(values), len(values))
	for i, value := range values {
		if err := assignValue(result.Index(i), value); err != nil {
			return fmt.Errorf("index %d: %w", i, err)
		}
	}
	target.Set(result)
	return nil
}

func parseFieldOptions(field reflect.StructField) fieldOptions {
	tag := field.Tag.Get("toml")
	if tag == "-" {
		return fieldOptions{ignore: true}
	}

	opts := fieldOptions{}
	if tag == "" {
		return opts
	}

	parts := strings.Split(tag, ",")
	if len(parts) > 0 && parts[0] != "" {
		opts.name = parts[0]
	}
	for _, part := range parts[1:] {
		switch strings.TrimSpace(part) {
		case "inline":
			opts.inline = true
		case "default":
			opts.defaultTop = true
		}
	}
	return opts
}

func isScalarKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return true
	default:
		return false
	}
}
