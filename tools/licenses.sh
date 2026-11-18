#!/bin/sh
# Collects the licence texts of everything PulseUSB is built from into
# internal/licenses/THIRD_PARTY_LICENSES.txt, which is embedded in the
# programs and shipped by the installers. Run from the repository root
# after changing dependencies:
#
#   sh tools/licenses.sh
set -e
out=internal/licenses/THIRD_PARTY_LICENSES.txt
section() { printf '\n\n================================================================\n%s\n================================================================\n\n' "$1"; }
{
  echo "PulseUSB includes or builds on the following third-party work."
  echo "Each is used under the licence reproduced below."

  section "usb.ids (USB vendor and product names), http://www.linux-usb.org/usb-ids.html
Maintained by Stephen J. Gowdy and contributors. Offered under the GNU GPL
version 2 or later, or the 3-clause BSD licence; used here under the latter."
  cat <<'BSD3'
Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice,
   this list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.
3. Neither the name of the copyright holder nor the names of its contributors
   may be used to endorse or promote products derived from this software
   without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
BSD3

  section "usbip-win2 (Windows USB/IP driver), https://github.com/vadimgrn/usbip-win2
Installed alongside the Windows app; not part of PulseUSB's own code."
  cat installer/vendor/usbip-win2-LICENSE.txt

  section "The Go programming language and standard library, https://go.dev"
  cat "$(go env GOROOT)/LICENSE"

  for mod in fyne.io/systray github.com/jchv/go-webview2 github.com/jchv/go-winloader \
             github.com/ebitengine/purego golang.org/x/sys github.com/godbus/dbus/v5; do
    dir=$(go list -m -f '{{.Dir}}' "$mod")
    ver=$(go list -m -f '{{.Version}}' "$mod")
    section "$mod $ver"
    for f in LICENSE LICENSE.md LICENSE.txt NOTICE; do
      [ -f "$dir/$f" ] && cat "$dir/$f" && echo
    done
  done
} > "$out"
echo "wrote $out ($(wc -l < "$out") lines)"
