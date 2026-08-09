/*
  The block editor.

  Movable stacks, in the Gutenberg sense: every piece of a post is a block you
  can reorder, change the type of, or delete, rather than a wall of markup you
  have to know the syntax for.

  Two decisions shape everything below.

  The first is that the block set is closed, and closed to exactly the things
  that survive a round trip through markdown: paragraph, heading, image, video,
  file, quote, code, bulleted list, numbered list, embed, and divider. That is
  not a starting point to grow from. Every block type that does not map cleanly
  onto the stored format is a data loss bug waiting for the right post to
  trigger it, so a type is only added here when its markdown form already
  exists.

  The second is that this serializes to markdown into a hidden field, which the
  ordinary form then submits. The server has no idea a block editor exists: it
  receives the same markdown it always did, through the same handler, with the
  same validation. That keeps one write path rather than two, and it means a
  bug in here can produce a bad post but never a corrupt one.

  Anything the parser does not recognise becomes a passthrough block, kept
  verbatim and written back untouched. A post written by hand with a table in
  it, or by a third-party client, opens in the editor and saves without being
  quietly rewritten.

  The plain textarea underneath remains the fallback. With this script blocked
  the editor never appears and the markdown is edited directly, which is also
  the escape hatch when a block gets in the way.
*/

(function () {
  "use strict";

  const root = document.getElementById("block-editor");
  const source = document.getElementById("editor-body");
  if (!root || !source) {
    return;
  }

  /* The textarea is the fallback, so it is only hidden once this has run. */
  const fallback = source.closest(".editor-fallback");
  if (fallback) {
    fallback.hidden = true;
  }
  root.hidden = false;

  /* ------------------------------------------------------------------ */
  /* The block types                                                     */
  /* ------------------------------------------------------------------ */

  /*
    Each type knows how to draw itself and how to write itself as markdown.
    Keeping those two next to each other is deliberate: they have to agree, and
    a type whose rendering and serialization live in different files is a type
    where they will eventually stop agreeing.
  */
  const TYPES = {
    paragraph: {
      label: "Text",
      menu: true,
      create: () => ({ type: "paragraph", text: "" }),
      toMarkdown: (b) => b.text,
    },
    heading: {
      label: "Heading",
      menu: true,
      create: () => ({ type: "heading", level: 2, text: "" }),
      /*
        Levels start at two. The post title is the page's only h1, and a second
        one inside the body breaks the document outline that screen readers and
        search engines rely on.
      */
      toMarkdown: (b) => "#".repeat(b.level || 2) + " " + b.text,
    },
    quote: {
      label: "Quote",
      menu: true,
      create: () => ({ type: "quote", text: "" }),
      toMarkdown: (b) =>
        b.text.split("\n").map((line) => "> " + line).join("\n"),
    },
    code: {
      label: "Code",
      menu: true,
      create: () => ({ type: "code", text: "", language: "" }),
      toMarkdown: (b) => "```" + (b.language || "") + "\n" + b.text + "\n```",
    },
    bulleted: {
      label: "Bulleted list",
      menu: true,
      create: () => ({ type: "bulleted", items: [""] }),
      toMarkdown: (b) => b.items.map((item) => "- " + item).join("\n"),
    },
    numbered: {
      label: "Numbered list",
      menu: true,
      create: () => ({ type: "numbered", items: [""] }),
      toMarkdown: (b) =>
        b.items.map((item, i) => String(i + 1) + ". " + item).join("\n"),
    },
    image: {
      /*
        One block type covers both a single photograph and a gallery of them.
        Adding a second image to a picture block is the whole gesture; there is
        no separate gallery type to choose in advance, because at the moment
        somebody inserts the first image they do not yet know whether a second
        one is coming.

        That choice shows up in the serialization: one image is written as
        ordinary markdown, and only two or more become a gallery directive. A
        post with a single photograph therefore stays a post with a plain
        "![]()" in it, readable by anything that reads markdown.
      */
      label: "Image",
      menu: true,
      create: () => ({ type: "image", images: [emptyImage()], caption: "" }),
      toMarkdown: (b) => {
        const images = usableImages(b);
        if (images.length === 0) return "";

        if (images.length === 1) {
          /*
            With one picture there is no gallery to caption, so the block's
            caption field and the picture's own are the same thing and only one
            of them can be holding text.
          */
          const only = images[0];
          const text = only.caption || b.caption;
          const caption = text ? ' "' + quoteSafe(text) + '"' : "";
          return "![" + (only.alt || "") + "](" + only.src + caption + ")";
        }

        let out = ":::gallery{";
        if (b.caption) out += 'caption="' + quoteSafe(b.caption) + '"';
        out += "}\n";
        out += images
          .map((image) => {
            const caption = image.caption
              ? ' "' + quoteSafe(image.caption) + '"'
              : "";
            return "![" + (image.alt || "") + "](" + image.src + caption + ")";
          })
          .join("\n");
        return out + "\n:::";
      },
    },
    video: {
      label: "Video",
      menu: true,
      create: () => ({ type: "video", src: "", poster: "", caption: "" }),
      toMarkdown: (b) => {
        if (!b.src) return "";
        let out = ':::video{src="' + b.src + '"';
        if (b.poster) out += ' poster="' + b.poster + '"';
        if (b.caption) out += ' caption="' + b.caption.replace(/"/g, "") + '"';
        return out + "}\n:::";
      },
    },
    file: {
      label: "File",
      menu: true,
      create: () => ({ type: "file", src: "", name: "" }),
      toMarkdown: (b) => {
        if (!b.src) return "";
        let out = ':::file{src="' + b.src + '"';
        if (b.name) out += ' name="' + b.name.replace(/"/g, "") + '"';
        return out + "}\n:::";
      },
    },
    embed: {
      label: "Embed",
      menu: true,
      create: () => ({ type: "embed", url: "", title: "" }),
      toMarkdown: (b) => {
        if (!b.url) return "";
        let out = ':::embed{url="' + b.url + '"';
        if (b.title) out += ' title="' + b.title.replace(/"/g, "") + '"';
        return out + "}\n:::";
      },
    },
    divider: {
      label: "Divider",
      menu: true,
      create: () => ({ type: "divider" }),
      toMarkdown: () => "---",
    },
    raw: {
      /*
        Not offered in the menu. This is what anything unrecognised becomes, so
        a table, a footnote, or some future markdown this editor has never heard
        of opens and saves without being touched. Without it, editing a
        hand-written post would silently delete whatever the parser could not
        model, which is the worst possible failure for a tool whose promise is
        that your files are yours.
      */
      label: "Markdown",
      menu: false,
      create: () => ({ type: "raw", text: "" }),
      toMarkdown: (b) => b.text,
    },
  };

  let blocks = [];

  function emptyImage() {
    return { src: "", alt: "", caption: "" };
  }

  /*
    The pictures in a block that are actually pointing at something. A row
    somebody opened and then left blank is not an image, and writing it out
    would put an empty "![]()" in the file.
  */
  function usableImages(block) {
    return (block.images || []).filter((image) => image && image.src);
  }

  /*
    Attribute values are written between double quotes, so a double quote
    inside one would end it early and the rest of the caption would be parsed
    as further attributes. Dropping the character is blunt, and it is what the
    other directives here already do; the alternative is an escaping rule that
    the hand-editable file format would then have to explain.
  */
  function quoteSafe(text) {
    return String(text).replace(/"/g, "");
  }

  /* ------------------------------------------------------------------ */
  /* Parsing markdown into blocks                                        */
  /* ------------------------------------------------------------------ */

  /*
    One markdown image occupying a whole line. Used both for a standalone
    picture and for each line inside a gallery, and shared so the two can never
    disagree about what counts as an image.

    This mirrors the pattern in the Go renderer, which is a duplication worth
    being aware of: the editor decides what to write and the renderer decides
    what to display, and if they drift then a gallery saved here shows up short
    on the page.
  */
  const IMAGE_LINE = /^\s*!\[([^\]]*)\]\(\s*(\S+?)(?:\s+"([^"]*)")?\s*\)\s*$/;

  function parse(markdown) {
    const lines = markdown.replace(/\r\n/g, "\n").split("\n");
    const out = [];
    let i = 0;

    while (i < lines.length) {
      const line = lines[i];

      if (line.trim() === "") {
        i++;
        continue;
      }

      /* Fenced code, which has to be checked before anything else because its
         contents can look like any other construct. */
      const fence = line.match(/^```(\w*)\s*$/);
      if (fence) {
        const language = fence[1] || "";
        const body = [];
        i++;
        while (i < lines.length && !/^```\s*$/.test(lines[i])) {
          body.push(lines[i]);
          i++;
        }
        i++; /* the closing fence */
        out.push({ type: "code", language: language, text: body.join("\n") });
        continue;
      }

      /* Container directives: video, file, embed, gallery. */
      const directive = line.match(/^:::(\w+)\{(.*)\}\s*$/);
      if (directive && ["video", "file", "embed", "gallery"].includes(directive[1])) {
        const attrs = parseAttributes(directive[2]);
        const inner = [];
        i++;
        while (i < lines.length && lines[i].trim() !== ":::") {
          inner.push(lines[i]);
          i++;
        }
        i++; /* the closing marker */

        if (directive[1] === "video") {
          out.push({ type: "video", src: attrs.src || "", poster: attrs.poster || "", caption: attrs.caption || "" });
        } else if (directive[1] === "file") {
          out.push({ type: "file", src: attrs.src || "", name: attrs.name || "" });
        } else if (directive[1] === "gallery") {
          const images = [];
          inner.forEach((entry) => {
            const found = entry.match(IMAGE_LINE);
            if (found) {
              images.push({ src: found[2], alt: found[1], caption: found[3] || "" });
            }
          });
          out.push({
            type: "image",
            images: images.length > 0 ? images : [emptyImage()],
            caption: attrs.caption || "",
          });
        } else {
          out.push({ type: "embed", url: attrs.url || "", title: attrs.title || "" });
        }
        continue;
      }

      if (/^---+\s*$/.test(line)) {
        out.push({ type: "divider" });
        i++;
        continue;
      }

      const heading = line.match(/^(#{1,6})\s+(.*)$/);
      if (heading) {
        /* Clamped to the range the renderer styles. A stored h1 or h5 is
           unusual but should not become an unstyled block. */
        const level = Math.min(Math.max(heading[1].length, 2), 4);
        out.push({ type: "heading", level: level, text: heading[2] });
        i++;
        continue;
      }

      /* An image alone on its line is a block; one inside a sentence is not,
         and stays part of the paragraph. */
      const image = line.match(IMAGE_LINE);
      if (image) {
        out.push({
          type: "image",
          images: [{ src: image[2], alt: image[1], caption: "" }],
          caption: image[3] || "",
        });
        i++;
        continue;
      }

      if (/^>\s?/.test(line)) {
        const body = [];
        while (i < lines.length && /^>\s?/.test(lines[i])) {
          body.push(lines[i].replace(/^>\s?/, ""));
          i++;
        }
        out.push({ type: "quote", text: body.join("\n") });
        continue;
      }

      if (/^[-*+]\s+/.test(line)) {
        const items = [];
        while (i < lines.length && /^[-*+]\s+/.test(lines[i])) {
          items.push(lines[i].replace(/^[-*+]\s+/, ""));
          i++;
        }
        out.push({ type: "bulleted", items: items });
        continue;
      }

      if (/^\d+[.)]\s+/.test(line)) {
        const items = [];
        while (i < lines.length && /^\d+[.)]\s+/.test(lines[i])) {
          items.push(lines[i].replace(/^\d+[.)]\s+/, ""));
          i++;
        }
        out.push({ type: "numbered", items: items });
        continue;
      }

      /* A table, or anything else with structure this editor cannot model, is
         kept whole rather than flattened into paragraphs. */
      if (/^\|/.test(line)) {
        const body = [];
        while (i < lines.length && lines[i].trim() !== "") {
          body.push(lines[i]);
          i++;
        }
        out.push({ type: "raw", text: body.join("\n") });
        continue;
      }

      /* Everything else is a paragraph, running until a blank line. */
      const paragraph = [];
      while (
        i < lines.length &&
        lines[i].trim() !== "" &&
        !/^(#{1,6}\s|>\s?|[-*+]\s|\d+[.)]\s|```|:::|---+\s*$|\|)/.test(lines[i])
      ) {
        paragraph.push(lines[i]);
        i++;
      }
      if (paragraph.length > 0) {
        out.push({ type: "paragraph", text: paragraph.join("\n") });
      } else {
        /* Guards against a line that matched none of the branches above and
           also failed the paragraph test, which would otherwise spin forever. */
        out.push({ type: "raw", text: lines[i] });
        i++;
      }
    }

    return out.length > 0 ? out : [TYPES.paragraph.create()];
  }

  function parseAttributes(text) {
    const attrs = {};
    const pattern = /(\w+)="([^"]*)"/g;
    let match;
    while ((match = pattern.exec(text)) !== null) {
      attrs[match[1]] = match[2];
    }
    return attrs;
  }

  /* ------------------------------------------------------------------ */
  /* Serializing blocks back to markdown                                 */
  /* ------------------------------------------------------------------ */

  function serialize() {
    return blocks
      .map((block) => {
        const type = TYPES[block.type] || TYPES.paragraph;
        return type.toMarkdown(block);
      })
      .filter((text) => text !== "")
      .join("\n\n");
  }

  /*
    The hidden textarea is what the form actually submits, so it is kept current
    on every change rather than only on submit. If anything ever goes wrong
    between here and the server, what was sent is visible in the fallback
    textarea rather than being assembled at the last moment out of sight.
  */
  function sync() {
    source.value = serialize();
  }


  /* ------------------------------------------------------------------ */
  /* Menus                                                               */
  /* ------------------------------------------------------------------ */

  /*
    A menu button, used for the block type, the heading level, and adding a
    block, so all three look and behave the same.

    Replacing a native select means giving up everything the browser did for
    free, and the keyboard handling below is the price of that. A select opens
    on Enter, moves with the arrows, closes on Escape, and returns focus when it
    does; a div with a click handler does none of it, and somebody navigating by
    keyboard simply cannot change a block's type. So all of that is
    reimplemented here rather than left out.

    aria-haspopup and aria-expanded are what tell a screen reader this is a menu
    and whether it is open, which is the other half of what the native element
    was providing.
  */
  function menuButton(options) {
    const wrapper = document.createElement("div");
    wrapper.className = "menu-wrap" + (options.wrapClass ? " " + options.wrapClass : "");

    const button = document.createElement("button");
    button.type = "button";
    button.className = options.buttonClass || "menu-button";
    button.textContent = options.label;
    button.title = options.title || options.label;
    button.setAttribute("aria-haspopup", "menu");
    button.setAttribute("aria-expanded", "false");
    if (options.ariaLabel) {
      button.setAttribute("aria-label", options.ariaLabel);
    }

    const menu = document.createElement("div");
    menu.className = "menu" + (options.menuClass ? " " + options.menuClass : "");
    menu.setAttribute("role", "menu");
    menu.hidden = true;

    options.items.forEach((item) => {
      const entry = document.createElement("button");
      entry.type = "button";
      entry.className = "menu-item";
      entry.setAttribute("role", "menuitem");
      entry.textContent = item.label;
      if (item.value === options.value) {
        entry.classList.add("is-current");
        entry.setAttribute("aria-current", "true");
      }
      entry.addEventListener("click", () => {
        close();
        options.onSelect(item.value);
      });
      menu.appendChild(entry);
    });

    function open(focusFirst) {
      /* Only one menu at a time, or two overlapping popups end up fighting
         over which is in front. */
      closeAllMenus();
      menu.hidden = false;
      button.setAttribute("aria-expanded", "true");
      if (focusFirst) {
        const first = menu.querySelector(".menu-item");
        if (first) first.focus();
      }
    }

    function close(returnFocus) {
      menu.hidden = true;
      button.setAttribute("aria-expanded", "false");
      if (returnFocus) {
        button.focus();
      }
    }

    button.addEventListener("click", (event) => {
      event.preventDefault();
      if (menu.hidden) {
        open(false);
      } else {
        close();
      }
    });

    button.addEventListener("keydown", (event) => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        open(true);
        return;
      }
      /*
        Opening by click leaves focus on the button rather than in the menu, so
        Escape has to be handled here as well. Without it the only way out of a
        menu you opened with the mouse is another click, which is not something
        somebody using the keyboard can do.
      */
      if (event.key === "Escape" && !menu.hidden) {
        event.preventDefault();
        close();
      }
    });

    menu.addEventListener("keydown", (event) => {
      const items = Array.from(menu.querySelectorAll(".menu-item"));
      const at = items.indexOf(document.activeElement);

      switch (event.key) {
        case "ArrowDown":
          event.preventDefault();
          items[(at + 1) % items.length].focus();
          break;
        case "ArrowUp":
          event.preventDefault();
          items[(at - 1 + items.length) % items.length].focus();
          break;
        case "Home":
          event.preventDefault();
          items[0].focus();
          break;
        case "End":
          event.preventDefault();
          items[items.length - 1].focus();
          break;
        case "Escape":
          event.preventDefault();
          close(true);
          break;
        case "Tab":
          /* Tabbing away is a decision to leave, so the menu closes rather than
             leaving a popup open behind the cursor. */
          close();
          break;
      }
    });

    wrapper.appendChild(button);
    wrapper.appendChild(menu);
    return wrapper;
  }

  /* Closes every open menu on the page. */
  function closeAllMenus() {
    root.querySelectorAll(".menu").forEach((menu) => {
      menu.hidden = true;
      const button = menu.previousElementSibling;
      if (button) {
        button.setAttribute("aria-expanded", "false");
      }
    });
  }

  /* A click anywhere else dismisses whatever is open. */
  document.addEventListener("click", (event) => {
    if (!event.target.closest(".menu-wrap")) {
      closeAllMenus();
    }
  });

  /* ------------------------------------------------------------------ */
  /* Rendering                                                           */
  /* ------------------------------------------------------------------ */

  function render() {
    root.innerHTML = "";
    blocks.forEach((block, index) => {
      root.appendChild(renderBlock(block, index));
    });
    root.appendChild(renderAddButton(blocks.length));
    sync();
  }

  function renderBlock(block, index) {
    const wrapper = document.createElement("div");
    wrapper.className = "bs-block bs-block-" + block.type;
    wrapper.dataset.index = String(index);
    wrapper.draggable = false;

    /*
      Content first, toolbar after. The toolbar used to float over the top
      right corner of the block, where it sat on top of whatever was written
      there. Below the content it is never in the way, and because it occupies
      real space rather than hovering, nothing shifts when it fades in.
    */
    const body = document.createElement("div");
    body.className = "block-body";
    renderBody(block, index, body);
    wrapper.appendChild(body);

    wrapper.appendChild(renderControls(block, index, wrapper));

    return wrapper;
  }

  function renderControls(block, index, wrapper) {
    const bar = document.createElement("div");
    bar.className = "block-controls";

    /*
      The drag handle is the only draggable part rather than the whole block.
      Making the block itself draggable means a text selection inside it starts
      a drag instead, which makes editing prose maddening.
    */
    const handle = document.createElement("button");
    handle.type = "button";
    handle.className = "block-handle";
    handle.title = "Drag to move";
    handle.setAttribute("aria-label", "Move block");
    handle.innerHTML = "&#8942;&#8942;";
    handle.addEventListener("mousedown", () => { wrapper.draggable = true; });
    handle.addEventListener("mouseup", () => { wrapper.draggable = false; });
    bar.appendChild(handle);

    /*
      Up and down buttons alongside the drag handle. Dragging is pleasant with a
      mouse and impossible with a keyboard, and these are the only way to
      reorder without one.
    */
    bar.appendChild(iconButton("↑", "Move up", () => move(index, index - 1), index === 0));
    bar.appendChild(iconButton("↓", "Move down", () => move(index, index + 1), index === blocks.length - 1));

    const types = Object.keys(TYPES)
      .filter((key) => TYPES[key].menu || key === block.type)
      .map((key) => ({ value: key, label: TYPES[key].label }));

    bar.appendChild(
      menuButton({
        label: TYPES[block.type] ? TYPES[block.type].label : "Text",
        ariaLabel: "Block type",
        title: "Change block type",
        items: types,
        value: block.type,
        buttonClass: "menu-button block-type",
        onSelect: (value) => convert(index, value),
      })
    );

    /*
      Pushed to the far end, so the one destructive control is nowhere near the
      ones used constantly.
    */
    const spacer = document.createElement("span");
    spacer.className = "block-spacer";
    bar.appendChild(spacer);

    bar.appendChild(iconButton("×", "Delete block", () => remove(index), false, "block-delete"));

    return bar;
  }

  function iconButton(glyph, label, onClick, disabled, extraClass) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "block-button" + (extraClass ? " " + extraClass : "");
    button.textContent = glyph;
    button.title = label;
    button.setAttribute("aria-label", label);
    button.disabled = Boolean(disabled);
    button.addEventListener("click", onClick);
    return button;
  }

  function renderBody(block, index, into) {
    switch (block.type) {
      case "heading": {
        /* The same menu widget as the type picker, so the two do not look like
           two different kinds of control doing the same kind of job. */
        into.appendChild(
          menuButton({
            label: "H" + (block.level || 2),
            ariaLabel: "Heading level",
            title: "Heading level",
            items: [2, 3, 4].map((n) => ({ value: String(n), label: "H" + n })),
            value: String(block.level || 2),
            buttonClass: "menu-button block-level",
            wrapClass: "block-level-wrap",
            onSelect: (value) => {
              block.level = Number(value);
              render();
              focusBlock(index);
            },
          })
        );
        into.appendChild(textArea(block, "text", "Heading", index, 1));
        break;
      }

      case "code":
        into.appendChild(textField(block, "language", "Language, optional"));
        into.appendChild(textArea(block, "text", "Code", index, 4, true));
        break;

      case "quote":
        into.appendChild(textArea(block, "text", "Quote", index, 2));
        break;

      case "bulleted":
      case "numbered":
        into.appendChild(listEditor(block, index));
        break;

      case "image":
        into.appendChild(galleryEditor(block, index));
        into.appendChild(
          textField(
            block,
            "caption",
            (block.images || []).length > 1
              ? "Caption for the gallery, optional"
              : "Caption, optional"
          )
        );
        break;

      case "video":
        into.appendChild(mediaPicker(block, "src", "Video", "video/mp4,video/webm"));
        into.appendChild(mediaPicker(block, "poster", "Poster image, optional", "image/*"));
        into.appendChild(textField(block, "caption", "Caption, optional"));
        break;

      case "file":
        into.appendChild(mediaPicker(block, "src", "File", ""));
        into.appendChild(textField(block, "name", "Name shown to readers"));
        break;

      case "embed":
        into.appendChild(textField(block, "url", "https://…"));
        into.appendChild(textField(block, "title", "Link text, optional"));
        break;

      case "divider": {
        const rule = document.createElement("hr");
        rule.className = "block-divider";
        into.appendChild(rule);
        break;
      }

      case "raw": {
        const note = document.createElement("p");
        note.className = "block-note";
        note.textContent = "Markdown this editor does not model. It is kept exactly as written.";
        into.appendChild(note);
        into.appendChild(textArea(block, "text", "Markdown", index, 3, true));
        break;
      }

      default:
        into.appendChild(textArea(block, "text", "Write here. Markdown works inline.", index, 2));
    }
  }

  function textField(block, key, placeholder) {
    const field = document.createElement("input");
    field.type = "text";
    field.className = "block-field";
    field.placeholder = placeholder;
    field.value = block[key] || "";
    field.addEventListener("input", () => {
      block[key] = field.value;
      sync();
    });
    return field;
  }

  function textArea(block, key, placeholder, index, rows, monospace) {
    const field = document.createElement("textarea");
    field.className = "block-text" + (monospace ? " is-mono" : "");
    field.placeholder = placeholder;
    field.rows = rows || 2;
    field.value = block[key] || "";

    /* Grows with its contents, so a long paragraph does not become a two-line
       window with its own scrollbar. */
    const grow = () => {
      field.style.height = "auto";
      field.style.height = field.scrollHeight + "px";
    };

    field.addEventListener("input", () => {
      block[key] = field.value;
      grow();
      sync();
    });

    field.addEventListener("keydown", (event) => {
      /*
        Enter on the last line of a paragraph starts a new block, which is what
        makes this feel like a block editor rather than a set of boxes.
        Shift+Enter still inserts a line break inside the block, and code and
        raw blocks keep Enter entirely since newlines are their content.
      */
      if (event.key !== "Enter" || event.shiftKey || monospace) {
        return;
      }
      if (block.type !== "paragraph") {
        return;
      }
      event.preventDefault();
      insert(index + 1, TYPES.paragraph.create());
    });

    /* Backspace in an empty block removes it and moves up, the way every block
       editor behaves. */
    field.addEventListener("keydown", (event) => {
      if (event.key !== "Backspace" || field.value !== "" || blocks.length <= 1) {
        return;
      }
      event.preventDefault();
      remove(index, index - 1);
    });

    window.requestAnimationFrame(grow);
    return field;
  }

  function listEditor(block, index) {
    const list = document.createElement("div");
    list.className = "block-list";

    (block.items || [""]).forEach((item, itemIndex) => {
      const row = document.createElement("div");
      row.className = "block-list-row";

      const marker = document.createElement("span");
      marker.className = "block-list-marker";
      marker.textContent = block.type === "numbered" ? String(itemIndex + 1) + "." : "•";
      row.appendChild(marker);

      const field = document.createElement("input");
      field.type = "text";
      field.className = "block-field";
      field.value = item;
      field.placeholder = "List item";

      field.addEventListener("input", () => {
        block.items[itemIndex] = field.value;
        sync();
      });

      field.addEventListener("keydown", (event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          block.items.splice(itemIndex + 1, 0, "");
          rerenderBlock(index, itemIndex + 1);
        } else if (event.key === "Backspace" && field.value === "" && block.items.length > 1) {
          event.preventDefault();
          block.items.splice(itemIndex, 1);
          rerenderBlock(index, Math.max(itemIndex - 1, 0));
        }
      });

      row.appendChild(field);
      list.appendChild(row);
    });

    return list;
  }

  /*
    The picture block's editor, which is one image or many depending on how
    many have been added. There is no moment where the author chooses "gallery"
    instead of "image"; adding a second picture is what makes it one, and
    removing the second turns it back.
  */
  function galleryEditor(block, index) {
    const wrapper = document.createElement("div");
    wrapper.className = "block-gallery";

    if (!Array.isArray(block.images) || block.images.length === 0) {
      block.images = [emptyImage()];
    }

    const multiple = block.images.length > 1;
    if (multiple) {
      wrapper.classList.add("is-gallery");
    }

    block.images.forEach((image, at) => {
      const item = document.createElement("div");
      item.className = "block-gallery-item";

      if (multiple) {
        /* Which slide this will be, since the order is the reading order and
           there is otherwise nothing on screen that says so. */
        const number = document.createElement("span");
        number.className = "block-gallery-number";
        number.textContent = String(at + 1);
        item.appendChild(number);
      }

      item.appendChild(mediaPicker(image, "src", "Image", "image/*"));
      item.appendChild(textField(image, "alt", "Description for screen readers"));

      /*
        Each slide carries its own caption, which is what the lightbox shows
        when that picture is the one on screen. On a single image there is
        nowhere for two captions to go, so the block's own field below covers
        it and this one would only be a second box meaning the same thing.
      */
      if (multiple) {
        item.appendChild(textField(image, "caption", "Caption for this picture, optional"));
      }

      /*
        Reordering and removal only appear once there is more than one picture.
        On a single image they would be a row of controls that either do
        nothing or empty the block, which is noise around the common case.
      */
      if (multiple) {
        const tools = document.createElement("div");
        tools.className = "block-gallery-tools";

        tools.appendChild(
          iconButton("←", "Move picture earlier", () => moveImage(block, index, at, at - 1), at === 0)
        );
        tools.appendChild(
          iconButton("→", "Move picture later", () => moveImage(block, index, at, at + 1), at === block.images.length - 1)
        );
        tools.appendChild(
          iconButton("×", "Remove picture", () => {
            block.images.splice(at, 1);
            if (block.images.length === 0) {
              block.images = [emptyImage()];
            }
            render();
            focusBlock(index);
          }, false, "block-delete")
        );

        item.appendChild(tools);
      }

      wrapper.appendChild(item);
    });

    const footer = document.createElement("div");
    footer.className = "block-gallery-footer";

    const addUploaded = document.createElement("button");
    addUploaded.type = "button";
    addUploaded.className = "button";
    addUploaded.textContent = multiple ? "Add pictures" : "Add another picture";
    addUploaded.addEventListener("click", () => {
      if (typeof window.broadsideUpload !== "function") {
        /* Without the dialog script there is still the empty row below and the
           path field on it, so a picture can be added by hand. */
        addBlankRow();
        return;
      }

      /*
        The dialog calls back once per file, so selecting eight photographs
        appends eight slides in the order they were chosen. The first one fills
        the blank row that is usually sitting there rather than leaving a gap
        above everything that follows.
      */
      window.broadsideUpload(
        function (url) {
          const blank = block.images.find((image) => !image.src);
          if (blank) {
            blank.src = url;
          } else {
            block.images.push({ src: url, alt: "" });
          }
          render();
        },
        "image/*",
        true
      );
    });
    footer.appendChild(addUploaded);

    const addBlank = document.createElement("button");
    addBlank.type = "button";
    addBlank.className = "button is-ghost";
    addBlank.textContent = "+";
    addBlank.title = "Add an empty row to paste a path into";
    addBlank.setAttribute("aria-label", "Add an empty picture row");
    addBlank.addEventListener("click", addBlankRow);
    footer.appendChild(addBlank);

    function addBlankRow() {
      block.images.push(emptyImage());
      render();

      /* Focus the path field of the row just added, rather than the top of the
         block, which is where focusBlock would have gone. */
      const items = root.querySelectorAll(
        '[data-index="' + index + '"] .block-gallery-item'
      );
      const added = items[items.length - 1];
      const field = added && added.querySelector(".block-field");
      if (field) {
        field.focus();
      }
    }

    wrapper.appendChild(footer);
    return wrapper;
  }

  function moveImage(block, index, from, to) {
    if (to < 0 || to >= block.images.length) {
      return;
    }
    const [image] = block.images.splice(from, 1);
    block.images.splice(to, 0, image);
    render();
    focusBlock(index);
  }

  function mediaPicker(block, key, label, accept) {
    const group = document.createElement("div");
    group.className = "block-media";

    const field = document.createElement("input");
    field.type = "text";
    field.className = "block-field";
    field.placeholder = label + " path";
    field.value = block[key] || "";
    field.addEventListener("input", () => {
      block[key] = field.value;
      updatePreview();
      sync();
    });

    const button = document.createElement("button");
    button.type = "button";
    button.className = "button";
    button.textContent = "Upload";
    button.addEventListener("click", () => {
      if (typeof window.broadsideUpload !== "function") {
        return;
      }
      window.broadsideUpload(function (url) {
        block[key] = url;
        field.value = url;
        updatePreview();
        sync();
      }, accept);
    });

    const row = document.createElement("div");
    row.className = "block-media-row";
    row.appendChild(field);
    row.appendChild(button);
    group.appendChild(row);

    const preview = document.createElement("img");
    preview.className = "block-preview";
    preview.alt = "";
    group.appendChild(preview);

    function updatePreview() {
      const value = block[key] || "";
      const isImage = /\.(jpe?g|png|gif|webp|avif)$/i.test(value);
      preview.hidden = !isImage;
      if (isImage) {
        preview.src = value;
      }
    }
    updatePreview();

    return group;
  }

  /* ------------------------------------------------------------------ */
  /* Mutating the stack                                                  */
  /* ------------------------------------------------------------------ */

  function renderAddButton(index) {
    const row = document.createElement("div");
    row.className = "block-add";

    const items = Object.keys(TYPES)
      .filter((key) => TYPES[key].menu)
      .map((key) => ({ value: key, label: TYPES[key].label }));

    row.appendChild(
      menuButton({
        label: "+",
        ariaLabel: "Add a block",
        title: "Add a block",
        items: items,
        buttonClass: "block-add-button",
        menuClass: "menu-grid",
        onSelect: (value) => insert(index, TYPES[value].create()),
      })
    );

    return row;
  }

  function insert(index, block) {
    blocks.splice(index, 0, block);
    render();
    focusBlock(index);
  }

  function remove(index, focusIndex) {
    if (blocks.length <= 1) {
      blocks = [TYPES.paragraph.create()];
      render();
      focusBlock(0);
      return;
    }
    blocks.splice(index, 1);
    render();
    focusBlock(typeof focusIndex === "number" ? Math.max(focusIndex, 0) : Math.max(index - 1, 0));
  }

  function move(from, to) {
    if (to < 0 || to >= blocks.length) {
      return;
    }
    const [block] = blocks.splice(from, 1);
    blocks.splice(to, 0, block);
    render();
    focusBlock(to);
  }

  function convert(index, type) {
    const block = blocks[index];
    const text = block.text || (block.items ? block.items.join("\n") : "");

    const converted = TYPES[type].create();

    /*
      Text is carried across wherever both sides have somewhere to put it, so
      changing a paragraph to a quote keeps what was written. Converting to a
      media block cannot carry text and starts empty, which is honest: there is
      nowhere for a sentence to go in an image block.
    */
    if ("text" in converted) {
      converted.text = text;
    } else if ("items" in converted) {
      converted.items = text ? text.split("\n") : [""];
    }

    blocks[index] = converted;
    render();
    focusBlock(index);
  }

  function rerenderBlock(index, focusItem) {
    render();
    const wrapper = root.querySelector('[data-index="' + index + '"]');
    if (!wrapper) {
      return;
    }
    const fields = wrapper.querySelectorAll(".block-field");
    const target = fields[focusItem] || fields[0];
    if (target) {
      target.focus();
    }
  }

  function focusBlock(index) {
    const wrapper = root.querySelector('[data-index="' + index + '"]');
    if (!wrapper) {
      return;
    }
    const field = wrapper.querySelector("textarea, input[type=text]");
    if (field) {
      field.focus();
      if (field.setSelectionRange) {
        const end = field.value.length;
        field.setSelectionRange(end, end);
      }
    }
  }

  /* ------------------------------------------------------------------ */
  /* Dragging                                                            */
  /* ------------------------------------------------------------------ */

  let dragIndex = null;

  root.addEventListener("dragstart", (event) => {
    const wrapper = event.target.closest(".bs-block");
    if (!wrapper) {
      return;
    }
    dragIndex = Number(wrapper.dataset.index);
    wrapper.classList.add("is-dragging");
    event.dataTransfer.effectAllowed = "move";
    /* Firefox will not start a drag unless something is set here. */
    event.dataTransfer.setData("text/plain", String(dragIndex));
  });

  root.addEventListener("dragover", (event) => {
    if (dragIndex === null) {
      return;
    }
    event.preventDefault();

    const over = event.target.closest(".bs-block");
    root.querySelectorAll(".bs-block").forEach((b) => b.classList.remove("is-over"));
    if (over && Number(over.dataset.index) !== dragIndex) {
      over.classList.add("is-over");
    }
  });

  root.addEventListener("drop", (event) => {
    if (dragIndex === null) {
      return;
    }
    event.preventDefault();

    const over = event.target.closest(".bs-block");
    if (over) {
      move(dragIndex, Number(over.dataset.index));
    }
    dragIndex = null;
  });

  root.addEventListener("dragend", () => {
    root.querySelectorAll(".bs-block").forEach((b) => {
      b.classList.remove("is-dragging", "is-over");
      b.draggable = false;
    });
    dragIndex = null;
  });

  /* ------------------------------------------------------------------ */
  /* Start                                                               */
  /* ------------------------------------------------------------------ */

  blocks = parse(source.value);
  render();

  /* Belt and braces: the hidden field is already current, but assembling it once
     more on submit costs nothing and removes any doubt about ordering. */
  const form = source.closest("form");
  if (form) {
    form.addEventListener("submit", sync);
  }
})();
