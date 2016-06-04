import sublime, sublime_plugin, subprocess, difflib
import os

"""
Sublime Text 3 - Gocode integration
Provides autocomplete and error highlighting by default, as well as a
few commands.

Example Key Bindings:

// Show a popup with information about the identifier at the cursor.
// Shows calltips if inside a function call.
  { "keys": ["alt+a"], "command": "gocode_popup_info",
    "context": [ { "key": "selector", "operator": "equal", "operand": "source.go", "match_all": true } ]
  },

// Go to definition.
  { "keys": ["alt+g"], "command": "gocode_goto_definition",
    "context": [ { "key": "selector", "operator": "equal", "operand": "source.go", "match_all": true } ]
  }

// Run goimports
  { "keys": ["ctrl+shift+i"], "command": "gocode_imports",
    "context": [ { "key": "selector", "operator": "equal", "operand": "source.go", "match_all": true } ]
  },

Error highlighting settings:
"""
UPDATE_ERRORS_ON_MODIFICATION = True
SHOW_ERRORS = True

# go to balanced pair, e.g.:
# ((abc(def)))
# ^
# \--------->^
#
# returns -1 on failure
def skip_to_balanced_pair(str, i, open, close):
	count = 1
	i += 1
	while i < len(str):
		if str[i] == open:
			count += 1
		elif str[i] == close:
			count -= 1

		if count == 0:
			break
		i += 1
	if i >= len(str):
		return -1
	return i

# split balanced parens string using comma as separator
# e.g.: "ab, (1, 2), cd" -> ["ab", "(1, 2)", "cd"]
# filters out empty strings
def split_balanced(s):
	out = []
	i = 0
	beg = 0
	while i < len(s):
		if s[i] == ',':
			out.append(s[beg:i].strip())
			beg = i+1
			i += 1
		elif s[i] == '(':
			i = skip_to_balanced_pair(s, i, "(", ")")
			if i == -1:
				i = len(s)
		else:
			i += 1

	out.append(s[beg:i].strip())
	return list(filter(bool, out))


def extract_arguments_and_returns(sig):
	sig = sig.strip()
	if not sig.startswith("func"):
		return [], []

	# find first pair of parens, these are arguments
	beg = sig.find("(")
	if beg == -1:
		return [], []
	end = skip_to_balanced_pair(sig, beg, "(", ")")
	if end == -1:
		return [], []
	args = split_balanced(sig[beg+1:end])

	# find the rest of the string, these are returns
	sig = sig[end+1:].strip()
	sig = sig[1:-1] if sig.startswith("(") and sig.endswith(")") else sig
	returns = split_balanced(sig)

	return args, returns

# takes gocode's candidate and returns sublime's hint and subj
def hint_and_subj(cls, name, type):
	subj = name
	if cls == "func":
		hint = cls + " " + name
		args, returns = extract_arguments_and_returns(type)
		if returns:
			hint += "\t" + ", ".join(returns)
		if args:
			sargs = []
			for i, a in enumerate(args):
				ea = a.replace("{", "\\{").replace("}", "\\}")
				sargs.append("${{{0}:{1}}}".format(i+1, ea))
			subj += "(" + ", ".join(sargs) + ")"
		else:
			subj += "()"
	else:
		hint = cls + " " + name + "\t" + type
	return hint, subj

def diff_sanity_check(a, b):
	if a != b:
		raise Exception("diff sanity check mismatch\n-%s\n+%s" % (a, b))

class GocodeGofmtCommand(sublime_plugin.TextCommand):
	def run(self, edit):
		view = self.view
		src = view.substr(sublime.Region(0, view.size()))
		gofmt = subprocess.Popen(["gofmt"],
			stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
		sout, serr = gofmt.communicate(src.encode())
		if gofmt.returncode != 0:
			print(serr.decode(), end="")
			return

		newsrc = sout.decode()
		diff = difflib.ndiff(src.splitlines(), newsrc.splitlines())
		i = 0
		for line in diff:
			if line.startswith("?"): # skip hint lines
				continue

			l = (len(line)-2)+1
			if line.startswith("-"):
				diff_sanity_check(view.substr(sublime.Region(i, i+l-1)), line[2:])
				view.erase(edit, sublime.Region(i, i+l))
			elif line.startswith("+"):
				view.insert(edit, i, line[2:]+"\n")
				i += l
			else:
				diff_sanity_check(view.substr(sublime.Region(i, i+l-1)), line[2:])
				i += l

class Gocode(sublime_plugin.EventListener):
	def on_query_completions(self, view, prefix, locations):
		loc = locations[0]
		if not view.match_selector(loc, "source.go"):
			return None

		src = view.substr(sublime.Region(0, view.size()))
		filename = view.file_name()
		cloc = "c{0}".format(loc)
		gocode = subprocess.Popen(["gocode", "-f=csv", "autocomplete", filename, cloc],
			stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
		try:
			result = gocode.communicate(src.encode(), timeout=1)
		except:
			return
		out = result[0].decode()
		result = []
		for line in filter(bool, out.split("\n")):
			arg = line.split(",,")
			hint, subj = hint_and_subj(*arg)
			result.append([hint, subj])

		return (result, sublime.INHIBIT_WORD_COMPLETIONS)

	def on_pre_save(self, view):
		if not view.match_selector(0, "source.go"):
			return
		view.run_command('gocode_gofmt')

class LookupResult:
	def __init__(self):
		self.pos, self.name, self.type, self.doc = "","","",""
		self.callarg = None

def lookup(view):
	view = view
	loc=view.sel()[0].a
	filename = view.file_name()
	src = view.substr(sublime.Region(0, view.size()))
	cloc = "c{0}".format(loc)
	gocode = subprocess.Popen(["gocode", "lookup", filename, cloc],
		stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
	try:
		result = gocode.communicate(src.encode(), timeout=1)
	except:
		return
	out = result[0].decode()
	lines = out.split("\n")
	ident,call=None,None
	cur=None
	for l in lines:
		parts = l.split(":", 1)
		if len(parts)!=2: break
		id, val = parts[0].strip(), parts[1].strip()
		if id == 'ident':
			ident=LookupResult()
			cur = ident
		elif id == 'call':
			call=LookupResult()
			cur=call
		if cur == None: continue
		if id == 'pos':
			cur.pos = val
		elif id == 'name':
			cur.name = val
		elif id == 'type':
			cur.type = val
		elif id == 'doc':
			cur.doc = val.replace("<BR>", "\n")
		elif id == 'callarg':
			cur.callarg = int(val)
	return ident, call

class GocodeGotoDefinition(sublime_plugin.TextCommand):
	def run(self, edit):
		ident,call=lookup(self.view)
		if ident is not None:
			sublime.active_window().open_file(ident.pos,sublime.ENCODED_POSITION)

html_escape_table = {
    "&": "&amp;",
    ">": "&gt;",
    "<": "&lt;",
    }

def html_escape(text):
    """Produce entities within text."""
    return "".join(html_escape_table.get(c,c) for c in text)

def show_type_info(res, showdoc=True, showpos=True, dim=False):
	nameStyle = '<b style="color:#ff5555">{}</b>'
	typeStyle = '<i style="color:#5555ff">{}</i>'
	if res.type.startswith("func("):
		func = extract_arguments_and_returns(res.type)
		args,ret = func
		desc = nameStyle.format(res.name)
		type_str="("
		for i, arg in enumerate(args):
			if i != 0:
				type_str += ","
			if i == res.callarg:
				type_str += '<b>{}</b>'.format(arg)
			else:
				type_str += arg
		type_str += ") "
		if len(ret)>1:
			type_str += '({})'.format(','.join(ret))
		else:
			type_str += ','.join(ret)
		desc += typeStyle.format(type_str)
	else:
		desc = nameStyle.format(res.name) + " " + typeStyle.format(res.type)
	bgcolor = "#dddddd" if not dim else "#cccccc"
	info = '<div style="font-size:12; padding:0px; margin:0px; background-color:{}">'.format(bgcolor)
	info += '<tt style="color:brown">{}</tt><br>'.format(desc)
	info += '<div style="font-size:10">'
	if showpos:
		pos=html_escape(res.pos)
		info += '  <a href="' + pos + '">'+pos+"</a><br>"
	if showdoc:
		doc=res.doc
		doclines=[l.lstrip("/").strip() for l in doc.split('\n')]
		if len(doclines)>8:	doc=' '.join(doclines[:8])
		else: doc=' '.join(doclines[:8])
		doc=html_escape(doc) #.replace("\n", "<br>")
		info += doc + "<br>"
	info += '</div>'
	info += '</div>'
	return info

class GocodePopupInfo(sublime_plugin.TextCommand):
	def run(self, edit):
		ident,call=lookup(self.view)
		if ident is None:
			if call is None:
				return
			else:
				info = show_type_info(call, showdoc=True)
		else:
			if call is None:
				info = show_type_info(ident, showdoc=True)
			else:
				info = show_type_info(call, showdoc=False, showpos=False)
				info += show_type_info(ident, showdoc=True, showpos=True, dim=True)
		self.view.show_popup(info, location=-1, max_width=1000, on_navigate=self.on_navigate)
	def on_navigate(self, pos):
		sublime.active_window().open_file(pos, sublime.ENCODED_POSITION)

# Highlighting on errors containing these strings is only done when the file
# is saved.
QUIET_ERRORS = ['declared but not used', 'imported but not used', 'is not used', 'missing return']

# Call gocode reporterrors after a file is changed and highlight errors.
class GocodeErrors(sublime_plugin.EventListener):
	def __init__(self):
		self.gosrc = False
		self.waiting=0
		self.errors = []

	def clear(self, view):
		self.errors=[]
		view.erase_status('gocurrenterror')
		view.erase_regions('gocodeerrors')
		view.erase_regions('gocodewarnings')
		view.erase_status('gocodeerrorcount')

	def on_activated(self, view):
		if not view.match_selector(0, "source.go"):
			self.gosrc=False
			return
		self.gosrc=True
		self.on_modified_async(view)

	def update_errors(self, view, show_all):
		if not SHOW_ERRORS: return
		self.clear(view)
		src = view.substr(sublime.Region(0, view.size()))
		filename = view.file_name()
		gocode = subprocess.Popen(["gocode", "reporterrors", filename],
			stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
		try:
			result = gocode.communicate(src.encode(), timeout=1)
		except:
			return
		out = result[0].decode()
		err_regions = []
		for line in out.split('\n'):
			if line.startswith("Error:"):
				parts=line.split()
				row,col=int(parts[1]), int(parts[2])
				pt = view.text_point(row-1, col-1)
				# When writing code, some errors are just annoying.
				if show_all or not any([(e in line) for e in QUIET_ERRORS]):
					err_regions.append(view.word(pt))

				self.errors.append((row-1, ' '.join(parts[3:])))
		view.add_regions('gocodeerrors', err_regions, "error", "dot",
			sublime.DRAW_NO_FILL | sublime.DRAW_NO_OUTLINE | sublime.DRAW_SQUIGGLY_UNDERLINE)
		err_count = len(err_regions)
		if err_count > 0:
			view.set_status('gocodeerrorcount', '** ' + str(err_count) + " ERRORS **")
			self.on_selection_modified_async(view)

	def on_selection_modified_async(self, view):
		if not self.gosrc: return
		row, col = view.rowcol(view.sel()[0].begin())
		view.erase_status('gocurrenterror')
		for e in self.errors:
			if e[0] == row:
				view.set_status('gocurrenterror', e[1])
	def on_post_save_async(self, view):
		if not self.gosrc: return
		if self.waiting == 0:
			self.update_errors(view, True)
	def on_modified_async(self, view):
		if not UPDATE_ERRORS_ON_MODIFICATION: return
		if not self.gosrc: return
		# 1 second after modifications, call update_errors.
		if self.waiting == 0:
			def cb():
				if self.waiting==1:
					self.waiting = 0
					self.update_errors(view, not view.is_dirty())
				else:
					self.waiting = 1
					sublime.set_timeout_async(cb, 1000)
			self.waiting = 1
			sublime.set_timeout_async(cb, 1000)
		else:
			self.waiting = 2

# Run goimports
class GocodeImportsCommand(sublime_plugin.TextCommand):
    def run(self, edit, saving=False):
        # Get the content of the current window from the text editor.
        selection = sublime.Region(0, self.view.size())
        content = self.view.substr(selection)

        process = subprocess.Popen(["goimports"],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                cwd=os.path.dirname(self.view.file_name()))
        process.stdin.write(bytes(content, 'utf8'))
        process.stdin.close()
        process.wait()

        # Put the result back.
        self.view.replace(edit, selection, process.stdout.read().decode('utf8'))
