package ucfg

import "reflect"

func (c *Config) Unpack(to interface{}, options ...Option) error {
	if c == nil {
		return ErrNilConfig
	}
	if to == nil {
		return ErrNilValue
	}

	opts := makeOptions(options)
	vTo := reflect.ValueOf(to)
	if to == nil || (vTo.Kind() != reflect.Ptr && vTo.Kind() != reflect.Map) {
		return ErrPointerRequired
	}
	return reifyInto(opts, vTo, c.fields)
}

func reifyInto(opts options, to reflect.Value, from map[string]value) error {
	to = chaseValuePointers(to)

	if to.Type() == tConfig {
		return mergeConfig(to.Addr().Interface().(*Config).fields, from)
	}

	switch to.Kind() {
	case reflect.Map:
		return reifyMap(opts, to, from)
	case reflect.Struct:
		return reifyStruct(opts, to, from)
	}

	return ErrTypeMismatch
}

func reifyMap(opts options, to reflect.Value, from map[string]value) error {
	if to.Type().Key().Kind() != reflect.String {
		return ErrTypeMismatch
	}

	if to.IsNil() {
		to.Set(reflect.MakeMap(to.Type()))
	}

	for k, value := range from {
		key := reflect.ValueOf(k)

		old := to.MapIndex(key)
		var v reflect.Value
		var err error

		if !old.IsValid() {
			v, err = reifyValue(opts, to.Type().Elem(), value)
		} else {
			v, err = reifyMergeValue(opts, old, value)
		}

		if err != nil {
			return err
		}
		to.SetMapIndex(key, v)
	}

	return nil
}

func reifyStruct(opts options, to reflect.Value, from map[string]value) error {
	to = chaseValuePointers(to)
	numField := to.NumField()

	for i := 0; i < numField; i++ {
		stField := to.Type().Field(i)
		name, _ := parseTags(stField.Tag.Get(opts.tag))
		name = fieldName(name, stField.Name)

		value, ok := from[name]
		if !ok {
			// TODO: handle missing config
			continue
		}

		vField := to.Field(i)
		v, err := reifyMergeValue(opts, vField, value)
		if err != nil {
			return err
		}
		vField.Set(v)
	}

	return nil
}

func reifyValue(opts options, t reflect.Type, val value) (reflect.Value, error) {
	if t.Kind() == reflect.Interface && t.NumMethod() == 0 {
		return reflect.ValueOf(val.reify()), nil
	}

	baseType := chaseTypePointers(t)
	if baseType == tConfig {
		if _, ok := val.(cfgSub); !ok {
			return reflect.Value{}, ErrTypeMismatch
		}

		v := val.reflect()
		if t == baseType { // copy config
			v = v.Elem()
		} else {
			v = pointerize(t, baseType, v)
		}
		return v, nil
	}

	if baseType.Kind() == reflect.Struct {
		if _, ok := val.(cfgSub); !ok {
			return reflect.Value{}, ErrTypeMismatch
		}

		newSt := reflect.New(baseType)
		if err := reifyInto(opts, newSt, val.(cfgSub).c.fields); err != nil {
			return reflect.Value{}, err
		}

		if t.Kind() != reflect.Ptr {
			return newSt.Elem(), nil
		}
		return pointerize(t, baseType, newSt), nil
	}

	if baseType.Kind() == reflect.Map {
		if _, ok := val.(cfgSub); !ok {
			return reflect.Value{}, ErrTypeMismatch
		}

		if baseType.Key().Kind() != reflect.String {
			return reflect.Value{}, ErrTypeMismatch
		}

		newMap := reflect.MakeMap(baseType)
		if err := reifyInto(opts, newMap, val.(cfgSub).c.fields); err != nil {
			return reflect.Value{}, err
		}
		return newMap, nil
	}

	if baseType.Kind() == reflect.Slice {
		arr, ok := val.(*cfgArray)
		if !ok {
			arr = &cfgArray{arr: []value{val}}
		}

		v, err := reifySlice(opts, baseType, arr)
		if err != nil {
			return reflect.Value{}, err
		}
		return pointerize(t, baseType, v), nil
	}

	v := val.reflect()
	if v.Type().ConvertibleTo(baseType) {
		v = pointerize(t, baseType, v.Convert(baseType))
		return v, nil
	}

	return reflect.Value{}, ErrTypeMismatch
}

func reifyMergeValue(
	opts options,
	oldValue reflect.Value, val value,
) (reflect.Value, error) {
	old := chaseValueInterfaces(oldValue)
	t := old.Type()
	old = chaseValuePointers(old)
	if (old.Kind() == reflect.Ptr || old.Kind() == reflect.Interface) && old.IsNil() {
		return reifyValue(opts, t, val)
	}

	baseType := chaseTypePointers(old.Type())
	if baseType == tConfig {
		sub, ok := val.(cfgSub)
		if !ok {
			return reflect.Value{}, ErrTypeMismatch
		}

		if t == baseType {
			// no pointer -> return type mismatch
			return reflect.Value{}, ErrTypeMismatch
		}

		// check if old is nil -> copy reference only
		if old.Kind() == reflect.Ptr && old.IsNil() {
			return pointerize(t, baseType, val.reflect()), nil
		}

		// check if old == value
		subOld := chaseValuePointers(old).Addr().Interface().(*Config)
		if sub.c == subOld {
			return oldValue, nil
		}

		// old != value -> merge value into old
		err := mergeConfig(subOld.fields, sub.c.fields)
		return oldValue, err
	}

	switch baseType.Kind() {
	case reflect.Map:
		sub, ok := val.(cfgSub)
		if !ok {
			return reflect.Value{}, ErrTypeMismatch
		}
		err := reifyMap(opts, old, sub.c.fields)
		return old, err
	case reflect.Struct:
		sub, ok := val.(cfgSub)
		if !ok {
			return reflect.Value{}, ErrTypeMismatch
		}
		err := reifyStruct(opts, old, sub.c.fields)
		return oldValue, err
	case reflect.Array:
		arr, ok := val.(*cfgArray)
		if !ok {
			// convert single value to array for merging
			arr = &cfgArray{
				arr: []value{val},
			}
		}
		return reifyArray(opts, old, baseType, arr)
	case reflect.Slice:
		arr, ok := val.(*cfgArray)
		if !ok {
			// convert single value to array for merging
			arr = &cfgArray{
				arr: []value{val},
			}
		}
		return reifySlice(opts, baseType, arr)
	}

	// try primitive conversion
	v := val.reflect()
	if v.Type().ConvertibleTo(baseType) {
		return pointerize(t, baseType, v.Convert(baseType)), nil
	}

	return reflect.Value{}, ErrTODO
}

func reifyArray(opts options, to reflect.Value, tTo reflect.Type, arr *cfgArray) (reflect.Value, error) {
	if arr.Len() != tTo.Len() {
		return reflect.Value{}, ErrArraySizeMistach
	}
	return reifyDoArray(opts, to, tTo.Elem(), arr)
}

func reifySlice(opts options, tTo reflect.Type, arr *cfgArray) (reflect.Value, error) {
	to := reflect.MakeSlice(tTo, arr.Len(), arr.Len())
	return reifyDoArray(opts, to, tTo.Elem(), arr)
}

func reifyDoArray(
	opts options,
	to reflect.Value, elemT reflect.Type, arr *cfgArray,
) (reflect.Value, error) {
	for i, from := range arr.arr {
		v, err := reifyValue(opts, elemT, from)
		if err != nil {
			return reflect.Value{}, ErrTODO
		}
		to.Index(i).Set(v)
	}
	return to, nil
}

func pointerize(t, base reflect.Type, v reflect.Value) reflect.Value {
	if t == base {
		return v
	}

	if t.Kind() == reflect.Interface {
		return v
	}

	for t != v.Type() {
		if !v.CanAddr() {
			tmp := reflect.New(v.Type())
			tmp.Elem().Set(v)
			v = tmp
		} else {
			v = v.Addr()
		}
	}
	return v
}
