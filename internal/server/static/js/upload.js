/*
  The upload dialog.

  One dialog serves every upload on the site: the picture and favicon fields in
  settings, and the image, video, and file blocks in the editor. Whatever opens
  it says what to do with the resulting path, so there is one piece of upload
  handling rather than one per place a file is needed.

  Everything here is an enhancement over markup that already works. The buttons
  that open it are anchors pointing at the Media tab, so with this blocked they
  still go somewhere a file can be uploaded. Nothing below is required to run the
  site.

  It runs under a content security policy that forbids inline script and eval,
  and has no dependencies.
*/

(function () {
  "use strict";

  const dialog = document.getElementById("upload-dialog");
  if (!dialog || typeof dialog.showModal !== "function") {
    return;
  }

  const input = document.getElementById("upload-dialog-input");
  const drop = document.getElementById("upload-drop");
  const status = document.getElementById("upload-status");
  const library = document.getElementById("upload-library");
  const heading = document.getElementById("upload-dialog-title");
  const dropText = drop.querySelector(".upload-drop-text");

  /*
    What to do with the path once a file lands. Set by whoever opened the
    dialog, so the same dialog can fill a settings field on one page and insert
    an image block on another.
  */
  let deliver = null;

  /*
    Whether this opening accepts more than one file. A gallery wants several at
    once; a favicon field wants exactly one, and offering multi-select there
    would only invite a choice the field cannot honour.
  */
  let allowMultiple = false;

  /*
    Where focus was before the dialog opened. A native dialog restores this
    itself in current browsers, but not in every version that is still around,
    and losing your place after an upload is a genuinely annoying way to fail.
  */
  let previouslyFocused = null;

  /* ------------------------------------------------------------------ */
  /* Opening and closing                                                 */
  /* ------------------------------------------------------------------ */

  function open(onDelivered, accept, multiple) {
    deliver = onDelivered;
    allowMultiple = Boolean(multiple);
    previouslyFocused = document.activeElement;

    /*
      The accepted types are narrowed by whoever opened it, so an image block
      offers images rather than every uploadable format. This only filters the
      file picker; the server sniffs the actual bytes regardless, because this
      attribute is trivially bypassed.
    */
    input.accept = accept || "image/*,video/mp4,video/webm,audio/*,application/pdf";
    input.multiple = allowMultiple;
    input.value = "";

    /*
      The wording follows the mode. A dialog that says "Drop a file here" while
      accepting twenty is quietly lying about what it will do.
    */
    if (heading) {
      heading.textContent = allowMultiple ? "Upload pictures" : "Upload a file";
    }
    if (dropText) {
      dropText.textContent = allowMultiple
        ? "Drop pictures here, or choose them"
        : "Drop a file here, or choose one";
    }

    setStatus("");
    dialog.showModal();
    loadLibrary();
  }

  function close() {
    dialog.close();
  }

  dialog.addEventListener("close", function () {
    deliver = null;
    allowMultiple = false;
    if (previouslyFocused && previouslyFocused.focus) {
      previouslyFocused.focus();
    }
  });

  /*
    Clicking the backdrop closes. A native dialog reports the backdrop as a
    click on the dialog element itself, so the test is whether the point is
    outside the dialog's own box rather than what the target is.
  */
  dialog.addEventListener("click", function (event) {
    if (event.target !== dialog) {
      return;
    }
    const box = dialog.getBoundingClientRect();
    const outside =
      event.clientX < box.left || event.clientX > box.right ||
      event.clientY < box.top || event.clientY > box.bottom;
    if (outside) {
      close();
    }
  });

  /* ------------------------------------------------------------------ */
  /* Uploading                                                           */
  /* ------------------------------------------------------------------ */

  function setStatus(message, isError) {
    status.textContent = message;
    status.classList.toggle("is-error", Boolean(isError));
  }

  /*
    Sends one file and resolves with its URL, or with null if it was rejected.
    The rejection is reported here rather than thrown, because a batch of eight
    photographs where the third is a HEIC the server will not take should still
    deliver the other seven.
  */
  function send(file, position, total) {
    const csrf = currentCSRF();
    if (!csrf) {
      setStatus("This page has no upload token. Reload and try again.", true);
      return Promise.resolve(null);
    }

    const body = new FormData();
    body.append("csrf", csrf);
    body.append("file", file);

    const counter = total > 1 ? " (" + position + " of " + total + ")" : "";
    setStatus("Uploading " + file.name + "…" + counter);

    /*
      Accept asks for JSON, which is what makes the server answer this the same
      way it answers an API client rather than redirecting back to the Media
      tab. One endpoint, two callers, identical validation.
    */
    return fetch("/admin/media/upload", {
      method: "POST",
      headers: { Accept: "application/json" },
      body: body,
    })
      .then(function (response) {
        return response.json().then(function (payload) {
          return { ok: response.ok, payload: payload };
        });
      })
      .then(function (result) {
        if (!result.ok || result.payload.error) {
          setStatus(
            (result.payload.error || "That file could not be uploaded.") +
              (total > 1 ? " (" + file.name + ")" : ""),
            true
          );
          return null;
        }
        return result.payload.url;
      })
      .catch(function () {
        setStatus("That upload failed. Check the connection and try again.", true);
        return null;
      });
  }

  /*
    Uploads are run one after another rather than all at once. Firing twenty
    parallel requests at a self-hosted box behind a domestic connection is a
    good way to have most of them time out, and the sequence also means the
    slides arrive in the order they were chosen instead of in whatever order
    the responses happen to come back.
  */
  function upload(files) {
    const list = Array.prototype.slice.call(files || []);
    if (list.length === 0) {
      return;
    }
    if (!allowMultiple) {
      list.length = 1;
    }

    drop.classList.add("is-busy");

    let failures = 0;

    list
      .reduce(function (chain, file, at) {
        return chain.then(function () {
          return send(file, at + 1, list.length).then(function (url) {
            if (url) {
              if (deliver) {
                deliver(url);
              }
            } else {
              failures += 1;
            }
          });
        });
      }, Promise.resolve())
      .finally(function () {
        drop.classList.remove("is-busy");

        /*
          The dialog stays open when something was rejected, so the message
          explaining why is still on screen. Closing it would leave the author
          with fewer pictures than they picked and nothing saying so.
        */
        if (failures === 0) {
          setStatus("");
          close();
        }
      });
  }

  function finish(url) {
    if (deliver) {
      deliver(url);
    }
    setStatus("");
    close();
  }

  /*
    The CSRF token is read from whatever form is on the page rather than held
    here, so it is always the current session's. Caching it would go stale the
    moment somebody signs out and back in within the same tab.
  */
  function currentCSRF() {
    const field = document.querySelector('input[name="csrf"]');
    return field ? field.value : "";
  }

  input.addEventListener("change", function () {
    upload(input.files);
  });

  /* ------------------------------------------------------------------ */
  /* Drag and drop                                                       */
  /* ------------------------------------------------------------------ */

  /*
    dragover has to be cancelled or the browser navigates to the dropped file,
    replacing the page with the image and losing whatever was being edited.
  */
  ["dragenter", "dragover"].forEach(function (type) {
    drop.addEventListener(type, function (event) {
      event.preventDefault();
      drop.classList.add("is-over");
    });
  });

  ["dragleave", "drop"].forEach(function (type) {
    drop.addEventListener(type, function (event) {
      event.preventDefault();
      drop.classList.remove("is-over");
    });
  });

  drop.addEventListener("drop", function (event) {
    upload(event.dataTransfer && event.dataTransfer.files);
  });

  /*
    A file dropped anywhere else on the page would otherwise navigate away from
    a half-written post, so it is swallowed rather than allowed through.
  */
  ["dragover", "drop"].forEach(function (type) {
    window.addEventListener(type, function (event) {
      if (!dialog.open) {
        event.preventDefault();
      }
    });
  });

  /* ------------------------------------------------------------------ */
  /* Picking something already uploaded                                  */
  /* ------------------------------------------------------------------ */

  /*
    Re-using a file is at least as common as adding one, and without this the
    only way to reference an existing upload is to go to the Media tab, copy a
    path, and come back.
  */
  function loadLibrary() {
    library.innerHTML = "";

    fetch("/api/media", { headers: { Accept: "application/json" } })
      .then(function (response) {
        return response.ok ? response.json() : { media: [] };
      })
      .then(function (payload) {
        const files = (payload.media || []).slice(0, 24);
        if (files.length === 0) {
          return;
        }

        files.forEach(function (file) {
          const item = document.createElement("li");
          const button = document.createElement("button");
          button.type = "button";
          button.className = "upload-library-item";
          button.title = file.name;

          if (/\.(jpe?g|png|gif|webp|avif)$/i.test(file.url)) {
            const image = document.createElement("img");
            image.src = file.url;
            image.alt = "";
            image.loading = "lazy";
            button.appendChild(image);
          } else {
            const label = document.createElement("span");
            label.className = "upload-library-name";
            label.textContent = file.name;
            button.appendChild(label);
          }

          button.addEventListener("click", function () {
            finish(file.url);
          });

          item.appendChild(button);
          library.appendChild(item);
        });
      })
      .catch(function () {
        /* An unreachable library is not worth an error; the picker still works. */
      });
  }

  /* ------------------------------------------------------------------ */
  /* Openers                                                             */
  /* ------------------------------------------------------------------ */

  /*
    Anything carrying data-upload-target opens the dialog and has the resulting
    path written into the field it names. Delegated, so a button added to the
    page later still works without rebinding.
  */
  document.addEventListener("click", function (event) {
    const opener = event.target.closest("[data-upload-target]");
    if (!opener) {
      return;
    }

    event.preventDefault();

    const target = document.getElementById(opener.dataset.uploadTarget);
    if (!target) {
      return;
    }

    open(function (url) {
      target.value = url;

      /*
        Announcing the change matters for anything watching the field, and it is
        what makes the colour pickers and any future live preview update rather
        than quietly disagreeing with what is in the box.
      */
      target.dispatchEvent(new Event("input", { bubbles: true }));
      target.dispatchEvent(new Event("change", { bubbles: true }));
    }, opener.dataset.uploadAccept);
  });

  /*
    Exposed so the editor can open the same dialog for its media blocks. This is
    the one global the scripts share, and it is a single function rather than an
    object so there is nothing to keep in sync.

    open(onDelivered, accept, multiple). With multiple set, onDelivered is
    called once per file, in the order they were chosen.
  */
  window.broadsideUpload = open;
})();
