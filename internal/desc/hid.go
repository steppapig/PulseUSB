package desc

// HIDKind reads a HID report descriptor (the description a keyboard, mouse
// or game controller gives of itself) and says what the device is:
// "Game controller", "Keyboard", "Mouse", "Keyboard and mouse", or "" when
// it is none of these (volume buttons on a headset, for example).
func HIDKind(rd []byte) string {
	controller, keyboard, mouse := false, false, false
	for _, u := range hidApplications(rd) {
		page, usage := u[0], u[1]
		switch {
		case page == 0x01 && (usage == 0x04 || usage == 0x05 || usage == 0x08): // joystick, gamepad, multi-axis
			controller = true
		case page == 0x02 || page == 0x05: // simulation controls (wheels, pedals), game controls
			controller = true
		case page == 0x01 && (usage == 0x06 || usage == 0x07): // keyboard, keypad
			keyboard = true
		case page == 0x01 && usage == 0x02: // mouse
			mouse = true
		}
	}
	switch {
	case controller:
		return "Game controller"
	case keyboard && mouse:
		return "Keyboard and mouse"
	case keyboard:
		return "Keyboard"
	case mouse:
		return "Mouse"
	}
	return ""
}

// hidApplications lists the usage page and usage of each top-level
// application collection in a report descriptor.
func hidApplications(rd []byte) [][2]uint32 {
	var out [][2]uint32
	var page, usage uint32
	haveUsage, depth := false, 0
	for i := 0; i < len(rd); {
		prefix := rd[i]
		if prefix == 0xfe { // long item: size in the next byte
			if i+1 >= len(rd) {
				break
			}
			i += 3 + int(rd[i+1])
			continue
		}
		size := int(prefix & 3)
		if size == 3 {
			size = 4
		}
		if i+1+size > len(rd) {
			break
		}
		var v uint32
		for k := 0; k < size; k++ {
			v |= uint32(rd[i+1+k]) << (8 * k)
		}
		typ, tag := (prefix>>2)&3, prefix>>4
		switch {
		case typ == 1 && tag == 0: // global: usage page
			page = v
		case typ == 2 && tag == 0: // local: usage (4 bytes include the page)
			if !haveUsage {
				usage, haveUsage = v, true
				if size == 4 {
					page, usage = v>>16, v&0xffff
				}
			}
		case typ == 0 && tag == 0xa: // main: collection
			if depth == 0 && v == 1 && haveUsage {
				out = append(out, [2]uint32{page, usage})
			}
			depth++
			haveUsage = false
		case typ == 0 && tag == 0xc: // main: end collection
			if depth > 0 {
				depth--
			}
		case typ == 0: // other main items end the local state
			haveUsage = false
		}
		i += 1 + size
	}
	return out
}

// HIDRelease reads a report descriptor and says whether an all-zero input
// report from this interface means "nothing pressed" and is safe to send
// when the device is taken away (keyboards, mice, media keys; not game
// controllers, where zero would move axes), and whether its reports start
// with a report number.
func HIDRelease(rd []byte) (releasable, reportIDs bool) {
	controller := false
	for _, u := range hidApplications(rd) {
		page, usage := u[0], u[1]
		switch {
		case page == 0x01 && (usage == 0x04 || usage == 0x05 || usage == 0x08), page == 0x02, page == 0x05:
			controller = true
		case page == 0x01 && (usage == 0x02 || usage == 0x06 || usage == 0x07 || usage == 0x80), page == 0x0c:
			releasable = true
		}
	}
	// A Report ID item (global, tag 8) anywhere means numbered reports.
	for i := 0; i < len(rd); {
		prefix := rd[i]
		if prefix == 0xfe {
			if i+1 >= len(rd) {
				break
			}
			i += 3 + int(rd[i+1])
			continue
		}
		size := int(prefix & 3)
		if size == 3 {
			size = 4
		}
		if prefix&0xfc == 0x84 {
			reportIDs = true
		}
		i += 1 + size
	}
	return releasable && !controller, reportIDs
}
