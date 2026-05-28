;; ;;; boson-mode.el --- A major mode for the Boson programming language -*- lexical-binding: t -*-

;; ;; Author: Andrea Orru <andreaorru1991@gmail.com>
;; ;;         Andrew Kelley <superjoe30@gmail.com>
;; ;;         Kyle Nusbaum <kjn@9project.net>
;; ;; Maintainer: Kyle Nusbaum <kjn@9project.net>
;; ;; URL: 
;; ;; Version: 0.0.1
;; ;; Package-Requires: ((emacs "26.1") (reformatter "0.6"))
;; ;; Keywords: boson, languages

;; ;; This file is free software; you can redistribute it and/or modify
;; ;; it under the terms of the GNU General Public License as published by
;; ;; the Free Software Foundation; either version 3, or (at your option)
;; ;; any later version.

;; ;; This file is distributed in the hope that it will be useful,
;; ;; but WITHOUT ANY WARRANTY; without even the implied warranty of
;; ;; MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
;; ;; GNU General Public License for more details.

;; ;; You should have received a copy of the GNU General Public License
;; ;; along with this program.  If not, see <http://www.gnu.org/licenses/>.

;; ;;; Commentary:

;; ;; A major mode for the Boson programming language.
;; ;; This is based on zig-mode available at:
;; ;; See documentation on https://github.com/ziglang/zig-mode

;; ;;; Code:

;; ;(require 'reformatter)

;; (defgroup boson-mode nil
;;   "Support for Boson code."
;;   :link '(url-link "https://bosonlang.org/")
;;   :group 'languages)

;; (defcustom boson-indent-offset 4
;;    "Indent Boson code by this number of spaces."
;;    :type 'integer
;;    :safe #'integerp)

;; ;(defcustom boson-format-on-save t
;; ;  "Format buffers before saving using boson fmt."
;; ;  :type 'boolean
;; ;  :safe #'booleanp)

;; ;(defcustom boson-boson-bin "boson"
;; ;  "Path to boson executable."
;; ;  :type 'file
;; ;  :safe #'stringp)

;; ;(defcustom boson-run-optimization-mode "Debug"
;; ;  "Optimization mode to run code with."
;; ;  :type 'string
;; ;  :safe #'stringp)

;; ;(defcustom boson-test-optimization-mode "Debug"
;; ;  "Optimization mode to run tests with."
;; ;  :type 'string
;; ;  :safe #'stringp)

;; ;; boson CLI commands

;; ;(defun boson--run-cmd (cmd &optional source &rest args)
;; ;  "Use compile command to execute a boson CMD with ARGS if given.
;; ;If given a SOURCE, execute the CMD on it."
;; ;  (let ((cmd-args (if source (cons source args) args)))
;; ;    (save-some-buffers)
;; ;    (compilation-start (mapconcat 'shell-quote-argument
;; ;                                  `(,boson-boson-bin ,cmd ,@cmd-args) " "))))

;; ;; ;;;###autoload
;; ;; (defun boson-compile ()
;; ;;   "Compile using `boson build`."
;; ;;   (interactive)
;; ;;   (boson--run-cmd "build"))

;; ;; ;;;###autoload
;; ;; (defun boson-build-exe ()
;; ;;   "Create executable from source or object file."
;; ;;   (interactive)
;; ;;   (boson--run-cmd "build-exe" (file-local-name (buffer-file-name))))

;; ;; ;;;###autoload
;; ;; (defun boson-build-lib ()
;; ;;   "Create library from source or assembly."
;; ;;   (interactive)
;; ;;   (boson--run-cmd "build-lib" (file-local-name (buffer-file-name))))

;; ;; ;;;###autoload
;; ;; (defun boson-build-obj ()
;; ;;   "Create object from source or assembly."
;; ;;   (interactive)
;; ;;   (boson--run-cmd "build-obj" (file-local-name (buffer-file-name))))

;; ;; ;;;###autoload
;; ;; (defun boson-test-buffer ()
;; ;;   "Test buffer using `boson test`."
;; ;;   (interactive)
;; ;;   (boson--run-cmd "test" (file-local-name (buffer-file-name)) "-O" boson-test-optimization-mode))

;; ;; ;;;###autoload
;; ;; (defun boson-run ()
;; ;;   "Create an executable from the current buffer and run it immediately."
;; ;;   (interactive)
;; ;;   (boson--run-cmd "run" (file-local-name (buffer-file-name)) "-O" boson-run-optimization-mode))

;; ;; boson fmt

;; ;; (reformatter-define boson-format
;; ;;   :program boson-boson-bin
;; ;;   :args '("fmt" "--stdin")
;; ;;   :group 'boson-mode
;; ;;   :lighter " BosonFmt")

;; ;;;###autoload (autoload 'boson-format-buffer "boson-mode" nil t)
;; ;;;###autoload (autoload 'boson-format-region "boson-mode" nil t)
;; ;;;###autoload (autoload 'boson-format-on-save-mode "boson-mode" nil t)

;; (defun boson-re-word (inner)
;;   "Construct a regular expression for the word INNER."
;;   (concat "\\<" inner "\\>"))

;; (defun boson-re-grab (inner)
;;   "Construct a group regular expression for INNER."
;;   (concat "\\(" inner "\\)"))

;; (defconst boson-re-optional "\\(?:[[:space:]]*\\?[[:space:]]*\\)")
;; (defconst boson-re-pointer "\\(?:[[:space:]]*\\*\\(?:const[[:space:]]*\\)?[[:space:]]*\\)")
;; (defconst boson-re-array "\\(?:[[:space:]]*\\[[^]]*\\]\\(?:const[[:space:]]*\\)?[[:space:]]*\\)")

;; (defconst boson-re-optionals-pointers-arrays
;;   (concat "\\(?:" boson-re-optional "\\|" boson-re-pointer "\\|" boson-re-array "\\)*"))

;; (defconst boson-re-identifier "[[:word:]_][[:word:]_[:digit:]]*")
;; (defconst boson-re-type "[[:word:]_.][[:word:]_.[:digit:]]*")
;; (defconst boson-re-type-annotation
;;   (concat (boson-re-grab boson-re-identifier)
;;           "[[:space:]]*:[[:space:]]*"
;;           boson-re-optionals-pointers-arrays
;;           (boson-re-grab boson-re-type)))

;; (defconst boson-re-block-label-open " \\([[:word:]_]+:\\)[[:space:]]*{")
;; (defconst boson-re-block-label-break "break[[:space:]]*\\(:[[:word:]_]+\\)")

;; (defun boson-re-definition (dtype)
;;   "Construct a regular expression for definitions of type DTYPE."
;;   (concat (boson-re-word dtype) "[[:space:]]+" (boson-re-grab boson-re-identifier)))

;; (defconst boson-mode-syntax-table
;;   (let ((table (make-syntax-table)))

;;     ;; Operators
;;     (dolist (i '(?+ ?- ?* ?/ ?% ?& ?| ?= ?! ?< ?>))
;;       (modify-syntax-entry i "." table))

;;     ;; Strings
;;     (modify-syntax-entry ?\' "\"" table)
;;     (modify-syntax-entry ?\" "\"" table)
;;     (modify-syntax-entry ?\\ "\\" table)

;;     ;; Comments
;;     (modify-syntax-entry ?/  ". 12" table)
;;     (modify-syntax-entry ?\n ">"    table)

;;     table))

;; (defconst boson-electric-indent-chars
;;   '(?\; ?\, ?\) ?\] ?\}))

(defface boson-multiline-string-face
  '((t :inherit font-lock-string-face))
  "Face for multiline string literals.")

;; (defvar boson-font-lock-keywords
;;   (append
;;    `(;; Builtins (prefixed with @)
;;      (,(concat "@" boson-re-identifier) . font-lock-builtin-face)

;;      ;; Keywords, constants and types
;;      (,(rx symbol-start
;;            (|
;;             ;; Storage
;;             "const" "var" "extern" "packed" "export" "pub" "noalias" "inline"
;;             "noinline" "comptime" "callconv" "volatile" "allowzero"
;;             "align" "linksection" "threadlocal" "addrspace"

;;             ;; Structure
;;             "struct" "enum" "union" "error" "opaque"

;;             ;; Statement
;;             "break" "return" "continue" "asm" "defer" "errdefer" "unreachable"
;;             "try" "catch" "async" "nosuspend" "await" "suspend" "resume"

;;             ;; Conditional
;;             "if" "else" "switch" "and" "or" "orelse"

;;             ;; Repeat
;;             "while" "for"

;;             ;; Other keywords
;;             "fn" "usingnamespace" "test")
;;            symbol-end)
;;       . font-lock-keyword-face)

;;      (,(rx symbol-start
;;            (|
;;             ;; Boolean
;;             "true" "false"

;;             ;; Other constants
;;             "null" "undefined")
;;            symbol-end)
;;       . font-lock-constant-face)

;;      (,(rx symbol-start
;;            (|
;;             ;; Integer types
;;             (: (any ?i ?u) (| ?0 (: (any (?1 . ?9)) (* digit))))
;;             "isize" "usize"

;;             ;; Floating types
;;             "f16" "f32" "f64" "f80" "f128"

;;             ;; C types
;;             "c_char" "c_short" "c_ushort" "c_int" "c_uint" "c_long" "c_ulong"
;;             "c_longlong" "c_ulonglong" "c_longdouble"

;;             ;; Comptime types
;;             "comptime_int" "comptime_float"

;;             ;; Other types
;;             "bool" "void" "noreturn" "type" "anyerror" "anyframe" "anytype"
;;             "anyopaque")
;;            symbol-end)
;;       . font-lock-type-face)

;;      ;; Block labels
;;      (,boson-re-block-label-open 1 font-lock-constant-face)
;;      (,boson-re-block-label-break 1 font-lock-constant-face)

;;      ;; Type annotations (both variable and type)
;;      (,boson-re-type-annotation 1 font-lock-variable-name-face)
;;      (,boson-re-type-annotation 2 font-lock-type-face))

;;    ;; Definitions
;;    (mapcar (lambda (x)
;;              (list (boson-re-definition (car x))
;;                    1 (cdr x)))
;;            '(("const" . font-lock-variable-name-face)
;;              ("var"   . font-lock-variable-name-face)
;;              ("fn"    . font-lock-function-name-face)))))

(defun boson-paren-nesting-level () (nth 0 (syntax-ppss)))
(defun boson-currently-in-str () (nth 3 (syntax-ppss)))
(defun boson-start-of-current-str-or-comment () (nth 8 (syntax-ppss)))

(defun boson-skip-backwards-past-whitespace-and-comments ()
  (while (or
          ;; If inside a comment, jump to start of comment.
          (let ((start (boson-start-of-current-str-or-comment)))
            (and start
                 (not (boson-currently-in-str))
                 (goto-char start)))
          ;; Skip backwards past whitespace and comment end delimiters.
          (/= 0 (skip-syntax-backward " >")))))

;; (defun boson-in-str-or-cmnt () (nth 8 (syntax-ppss)))
;; (defconst boson-top-item-beg-re
;;   (concat "^ *"
;;           (regexp-opt
;;            '("pub" "extern" "export" ""))
;;           "[[:space:]]*"
;;           (regexp-opt
;;            '("fn" "test"))
;;           "[[:space:]]+")
;;   "Start of a Boson item.")

;; (defun boson-beginning-of-defun (&optional arg)
;;   "Move backward to the beginning of the current defun.

;; With ARG, move backward multiple defuns.  Negative ARG means
;; move forward.

;; This is written mainly to be used as `beginning-of-defun-function' for Boson."
;;   (interactive "p")
;;   (let* ((arg (or arg 1))
;;          (magnitude (abs arg))
;;          (sign (if (< arg 0) -1 1)))
;;     ;; If moving forward, don't find the defun we might currently be
;;     ;; on.
;;     (when (< sign 0)
;;       (end-of-line))
;;     (catch 'done
;;       (dotimes (_ magnitude)
;;         ;; Search until we find a match that is not in a string or comment.
;;         (while (if (re-search-backward (concat "^[[:space:]]*\\(" boson-top-item-beg-re "\\)")
;;                                        nil 'move sign)
;;                    (boson-in-str-or-cmnt)
;;                  ;; Did not find it.
;;                  (throw 'done nil)))))
;;     t))

;; (defun boson-end-of-defun ()
;;   "Move forward to the next end of defun.

;; With argument, do it that many times.
;; Negative argument -N means move back to Nth preceding end of defun.

;; Assume that this is called after `beginning-of-defun'.  So point is
;; at the beginning of the defun body.

;; This is written mainly to be used as `end-of-defun-function' for Boson."
;;   (interactive)

;;   ;; Jump over the function parameters and paren-wrapped return, if they exist.
;;   (while (re-search-forward "(" (line-end-position) t)
;;     (progn
;;       (backward-char)
;;       (forward-sexp)))

;;   ;; Find the opening brace
;;   (if (re-search-forward "[{]" nil t)
;;       (progn
;;         (goto-char (match-beginning 0))
;;         ;; Go to the closing brace
;;         (condition-case nil
;;             (forward-sexp)
;;           (scan-error
;;            (goto-char (point-max))))
;;         (end-of-line))
;;     ;; There is no opening brace, so consider the whole buffer to be one "defun"
;;     (goto-char (point-max))))

(defun boson-mode-indent-line ()
  (interactive)
  ;; First, calculate the column that this line should be indented to.
  (let ((indent-col
         (save-excursion
           (back-to-indentation)
           (let* (;; paren-level: How many sets of parens (or other delimiters)
                  ;;   we're within, except that if this line closes the
                  ;;   innermost set(s) (e.g. the line is just "}"), then we
                  ;;   don't count those set(s).
                  (paren-level
                   (save-excursion
                     (while (looking-at "[]})]") (forward-char))
                     (boson-paren-nesting-level)))
                  ;; prev-block-indent-col: If we're within delimiters, this is
                  ;; the column to which the start of that block is indented
                  ;; (if we're not, this is just zero).
                  (prev-block-indent-col
                   (if (<= paren-level 0) 0
                     (save-excursion
                       (while (>= (boson-paren-nesting-level) paren-level)
                         (backward-up-list)
                         (back-to-indentation))
                       (current-column))))
                  ;; base-indent-col: The column to which a complete expression
                  ;;   on this line should be indented.
                  (base-indent-col
                   (if (<= paren-level 0)
                       prev-block-indent-col
                     (or (save-excursion
                           (backward-up-list)
                           (forward-char)
                           (and (not (looking-at " *\\(//[^\n]*\\)?"))
                                (current-column)))
                         (+ prev-block-indent-col tab-width)))) ;boson-indent-offset))))
                  ;; is-expr-continuation: True if this line continues an
                  ;; expression from the previous line, false otherwise.
                  (is-expr-continuation
                   (and
                    (not (looking-at "[]});]\\|else"))
                    (save-excursion
                      (boson-skip-backwards-past-whitespace-and-comments)
                      (when (> (point) 1)
                        (backward-char)
                        (or (boson-currently-in-str)
                            (not (looking-at "[,;([{}]"))))))))
             ;; Now we can calculate indent-col:
             (if nil ;is-expr-continuation
                 (+ base-indent-col tab-width) ;boson-indent-offset)
               base-indent-col)))))
    ;; If point is within the indentation whitespace, move it to the end of the
    ;; new indentation whitespace (which is what the indent-line-to function
    ;; always does).  Otherwise, we don't want point to move, so we use a
    ;; save-excursion.
    (if (<= (current-column) (current-indentation))
        (indent-line-to indent-col)
      (save-excursion (indent-line-to indent-col)))))

;; (defun boson-syntax-propertize-multiline-string (end)
;;   (let* ((eol (save-excursion (search-forward "\n" end t)))
;;          (stop (or eol end)))
;;     (while (search-forward "\\" stop t)
;;       (put-text-property (match-beginning 0) (match-end 0) 'syntax-table (string-to-syntax ".")))
;;     (when eol (put-text-property (- eol 2) (1- eol) 'syntax-table (string-to-syntax "|")))
;;     (goto-char stop)))

;; (defun boson-syntax-propertize (start end)
;;   (goto-char start)
;;   (when (eq t (boson-currently-in-str))
;;     (boson-syntax-propertize-multiline-string end))
;;   (while (search-forward "\\\\" end t)
;;     (when (null (save-excursion (backward-char 2) (boson-currently-in-str)))
;;       (backward-char)
;;       (put-text-property (match-beginning 0) (point) 'syntax-table (string-to-syntax "|"))
;;       (boson-syntax-propertize-multiline-string end))))

(defun boson-mode-syntactic-face-function (state)
  (save-excursion
    (goto-char (nth 8 state))
    (if (nth 3 state)
        (if (looking-at "\\\\\\\\")
            'boson-multiline-string-face
          'font-lock-string-face)
      (if (looking-at "//[/|!][^/]")
          'font-lock-doc-face
        'font-lock-comment-face))))

;; ;;; Imenu support
;; (defun boson-re-structure-def-imenu (stype)
;;   "Construct a regular expression for strucutres definitions of type STYPE."
;;   (concat (boson-re-word "const") "[[:space:]]+"
;;           (boson-re-grab boson-re-identifier)
;;           ".*"
;;           (boson-re-word stype)))

;; (defvar boson-imenu-generic-expression
;;   (append (mapcar (lambda (x)
;;                     (list (capitalize x) (boson-re-structure-def-imenu x) 1))
;;                   '("enum" "struct" "union"))
;;           `(("Fn" ,(boson-re-definition "fn") 1))))

;; (defvar boson-mode-map
;;   (let ((map (make-sparse-keymap)))
;; ;    (define-key map (kbd "C-c C-b") #'boson-compile)
;; ;    (define-key map (kbd "C-c C-f") #'boson-format-buffer)
;; ;    (define-key map (kbd "C-c C-r") #'boson-run)
;; ;    (define-key map (kbd "C-c C-t") #'boson-test-buffer)
;;     map)
;;   "Keymap for Boson major mode.")

;; ;;;###autoload
;; (define-derived-mode boson-mode prog-mode "Boson"
;;   "A major mode for the Boson programming language."
;;   (setq-local comment-start "// ")
;;   (setq-local comment-start-skip "//+ *")
;;   (setq-local comment-end "")
;;   (setq-local electric-indent-chars
;;               (append boson-electric-indent-chars
;;                       (and (boundp 'electric-indent-chars)
;;                            electric-indent-chars)))
;;   (setq-local beginning-of-defun-function 'boson-beginning-of-defun)
;;   (setq-local end-of-defun-function 'boson-end-of-defun)
;;   (setq-local indent-line-function 'boson-mode-indent-line)
;;   (setq-local indent-tabs-mode t)  ; Boson forbids tab characters.
;;   (setq-local syntax-propertize-function 'boson-syntax-propertize)
;;   (setq-local imenu-generic-expression boson-imenu-generic-expression)
;;   (setq-local compile-command "boson build")
;;   (setq buffer-file-coding-system 'utf-8-unix) ; boson source is always utf-8
;;   (setq font-lock-defaults '(boson-font-lock-keywords
;;                              nil nil nil nil
;;                              (font-lock-syntactic-face-function . boson-mode-syntactic-face-function)))

;; 					;(when boson-format-on-save
;; 					;(boson-format-on-save-mode 1)))
;;   )


;; (defconst boson--font-lock-defaults
;;   (let ((keywords '("var"
;; 		    "fn"
;; 		    "for"
;; 		    "if"
;; 		    "else"
;; 		    "return"
;; 		    "break"
;; 		    "struct"
;; 		    "import"
;; 		    "package"))
;; 	(types '("str" "num" "void")))
;;     `(((,(rx-to-string `(: (or ,@keywords))) 0 font-lock-keyword-face)
;;        ("\\([[:word:]]+\\)\s*(" 1 font-lock-function-name-face)
;;        (,(rx-to-string `(: (or ,@types))) 0 font-lock-type-face)))))

(defun boson-re-word (inner)
  "Construct a regular expression for the word INNER."
  (concat "\\<" inner "\\>"))

(defun boson-re-grab (inner)
  "Construct a group regular expression for INNER."
  (concat "\\(" inner "\\)"))

(defconst boson-re-optional "\\(?:[[:space:]]*\\?[[:space:]]*\\)")
(defconst boson-re-pointer "\\(?:[[:space:]]*\\*\\(?:const[[:space:]]*\\)?[[:space:]]*\\)")
(defconst boson-re-array "\\(?:[[:space:]]*\\[[^]]*\\]\\(?:const[[:space:]]*\\)?[[:space:]]*\\)")

(defconst boson-re-optionals-pointers-arrays
  (concat "\\(?:" boson-re-optional "\\|" boson-re-pointer "\\|" boson-re-array "\\)*"))

(defconst boson-re-identifier "[[:word:]_][[:word:]_[:digit:]]*")
(defconst boson-re-type "[[:word:]_.][[:word:]_.[:digit:]]*")
(defconst boson-re-type-annotation
  (concat (boson-re-grab boson-re-identifier)
          "[[:space:]]*:[[:space:]]*"
          boson-re-optionals-pointers-arrays
          (boson-re-grab boson-re-type)))

(defconst boson-re-block-label-open " \\([[:word:]_]+:\\)[[:space:]]*{")
(defconst boson-re-block-label-break "break[[:space:]]*\\(:[[:word:]_]+\\)")

(defun boson-re-definition (dtype)
  "Construct a regular expression for definitions of type DTYPE."
  (concat (boson-re-word dtype) "[[:space:]]+" (boson-re-grab boson-re-identifier)))

(defvar boson--font-lock-keywords
  (append
   `(;; Keywords
     (,(rx symbol-start
           (|
            ;; Declarations
            "fn" "var" "const" "type" "struct" "interface"
            ;; Qualifiers
            "mut" "owned" "dispose"
            ;; Control flow
            "if" "else" "for" "break" "continue" "return"
            ;; Imports
            "import")
           symbol-end)
      . font-lock-keyword-face)

     ;; Constants
     (,(rx symbol-start
           (| "true" "false" "nil")
           symbol-end)
      . font-lock-constant-face)

     ;; Built-in scalar types
     (,(rx symbol-start
           (| "i8" "i16" "i32" "i64"
              "u8" "u16" "u32" "u64"
              "byte" "bool" "void")
           symbol-end)
      . font-lock-type-face)
	 
     ;; Block labels
     (,boson-re-block-label-open 1 font-lock-constant-face)
     (,boson-re-block-label-break 1 font-lock-constant-face)
	 
     ;; Type annotations (both variable and type)
     (,boson-re-type-annotation 1 font-lock-variable-name-face)
     (,boson-re-type-annotation 2 font-lock-type-face))
   
   ;; Definitions
   (mapcar (lambda (x)
             (list (boson-re-definition (car x))
                   1 (cdr x)))
           '(;("const" . font-lock-variable-name-face)
             ("var"   . font-lock-variable-name-face)
             ("fn"    . font-lock-function-name-face)))))
  
(setf boson-mode-syntax-table
  (let ((st (make-syntax-table)))
    (modify-syntax-entry ?\{ "(}" st)
    (modify-syntax-entry ?\( "()" st)

    ;; Operators
    (dolist (i '(?+ ?- ?* ?/ ?% ?& ?| ?= ?! ?< ?>))
      (modify-syntax-entry i "." st))
    
    ;; Strings
    (modify-syntax-entry ?\' "\"" st)
    (modify-syntax-entry ?\" "\"" st)
    (modify-syntax-entry ?\\ "\\" st)
    
    ;; Comments
    (modify-syntax-entry ?/  ". 12" st)
    (modify-syntax-entry ?\n ">"    st)
    
    st))

(defvar boson-mode-abbrev-table nil
  "Abbreviation table used in `boson-mode' buffers.")

(define-abbrev-table 'boson-mode-abbrev-table
  '())

;;;###autoload
(define-derived-mode boson-mode prog-mode "boson"
  "Major mode for boson files."
  :abbrev-table boson-mode-abbrev-table
										;(setq font-lock-defaults boson--font-lock-keywords)
  (setq font-lock-defaults '(boson--font-lock-keywords
                             nil nil nil nil
                             (font-lock-syntactic-face-function . boson-mode-syntactic-face-function)))
  (setq-local comment-start "//")
  (setq-local comment-start-skip "//+ *")
  (setq-local indent-line-function #'boson-mode-indent-line)
  ;(setq-local tab-width boson-indent-offset)
  (setq-local indent-tabs-mode t))


;;;###autoload
(add-to-list 'auto-mode-alist '("\\.\\(bos\\|bosc\\)\\'" . boson-mode))

(provide 'boson-mode)
;;; boson-mode.el ends here
