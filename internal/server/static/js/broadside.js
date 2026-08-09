/*
  Progressive enhancement for the public site.

  Nothing here is required to read Broadside. Every image is already a link to
  the full-size file and every "older posts" control is already a real anchor,
  so with this script blocked the site keeps working, just with a page load
  where an overlay or an append would have been. That property is worth
  protecting: it is what keeps the site usable in a text browser, readable by a
  crawler that runs no scripts, and functional when a CDN has a bad afternoon.

  There are no dependencies and no build step. The file is served as-is and
  runs under a content security policy that forbids inline script and eval.
*/

(function () {
  "use strict";

  /* ------------------------------------------------------------------ */
  /* Lightbox                                                            */
  /* ------------------------------------------------------------------ */

  /*
    The overlay markup already exists in the page, placed there by the layout
    template. Building it here instead would mean either injecting a style
    attribute, which the content security policy forbids, or duplicating the
    styling rules in JavaScript where nobody would find them.
  */
  const lightbox = document.getElementById("lightbox");

  if (lightbox) {
    const image = lightbox.querySelector(".lightbox-image");
    const caption = lightbox.querySelector(".lightbox-caption");
    const closeButton = lightbox.querySelector(".lightbox-close");
    const prevButton = lightbox.querySelector(".lightbox-prev");
    const nextButton = lightbox.querySelector(".lightbox-next");

    /*
      The set of images the overlay can page through, and where in it we are.
      This is recomputed on open rather than cached, because infinite scroll
      adds images to the page after this script has run.
    */
    let gallery = [];
    let position = 0;

    /*
      Remembers what had focus before the overlay opened, so it can be restored
      on close. Without this, dismissing the overlay drops keyboard focus back
      to the top of the document and a keyboard user loses their place.
    */
    let previouslyFocused = null;

    /*
      Which images the overlay pages through depends on where the reader came
      in. A click inside a carousel pages through that carousel and stops at its
      ends, because a gallery is a set the author grouped on purpose and walking
      out of it into an unrelated photograph three posts down is not what
      pressing the right arrow was asking for. A click on a standalone image
      pages through the standalone images, with the carousels left out for the
      same reason.
    */
    function collectGallery(from) {
      const carousel = from && from.closest(".bs-gallery");
      if (carousel) {
        return Array.from(carousel.querySelectorAll("a[data-lightbox]"));
      }

      return Array.from(document.querySelectorAll("a[data-lightbox]")).filter(
        function (link) {
          return !link.closest(".bs-gallery");
        }
      );
    }

    function show(index) {
      if (index < 0 || index >= gallery.length) {
        return;
      }

      position = index;
      const link = gallery[position];

      image.src = link.getAttribute("href");

      /*
        The alt text comes from the thumbnail rather than being invented here,
        so a screen reader hears the same description in the overlay that it
        would on the page.
      */
      const thumbnail = link.querySelector("img");
      image.alt = thumbnail ? thumbnail.alt : "";

      caption.textContent = link.getAttribute("data-caption") || "";
    }

    function open(link) {
      gallery = collectGallery(link);

      const index = gallery.indexOf(link);
      if (index < 0) {
        return;
      }

      previouslyFocused = document.activeElement;

      show(index);

      /*
        A single image needs no navigation arrows, and showing controls that do
        nothing is worse than showing none.
      */
      lightbox.classList.toggle("is-single", gallery.length < 2);

      lightbox.hidden = false;
      lightbox.setAttribute("aria-hidden", "false");

      /*
        The class is applied on the next frame rather than immediately.
        Removing the hidden attribute and adding the class in the same frame
        means the browser has no previous state to transition from, so the fade
        would not run at all.
      */
      requestAnimationFrame(function () {
        lightbox.classList.add("is-open");
      });

      /*
        Stop the page behind the overlay from scrolling. Without this, a scroll
        gesture over the overlay moves the timeline underneath it, and closing
        the overlay leaves the reader somewhere they did not expect.
      */
      document.body.style.overflow = "hidden";

      closeButton.focus();
    }

    function close() {
      lightbox.classList.remove("is-open");
      lightbox.setAttribute("aria-hidden", "true");
      document.body.style.overflow = "";

      /*
        Wait for the fade to finish before hiding, otherwise the overlay
        disappears instantly and the transition is never seen. The delay
        matches the duration in the stylesheet.
      */
      window.setTimeout(function () {
        lightbox.hidden = true;
        /*
          Clearing the source releases the decoded image. On a long photo-heavy
          timeline this is the difference between steady memory use and slowly
          climbing memory use.
        */
        image.src = "";
      }, 200);

      if (previouslyFocused && previouslyFocused.focus) {
        previouslyFocused.focus();
      }
    }

    /* Wrapping at both ends makes the gallery feel continuous. */
    function next() {
      show((position + 1) % gallery.length);
    }

    function previous() {
      show((position - 1 + gallery.length) % gallery.length);
    }

    /*
      One delegated listener on the document rather than one per image. This
      matters because infinite scroll adds images after load, and a delegated
      listener picks those up with no rebinding.
    */
    document.addEventListener("click", function (event) {
      const link = event.target.closest("a[data-lightbox]");
      if (!link) {
        return;
      }

      /*
        Modified clicks are left alone so that "open in new tab" and the
        equivalent gestures still reach the full-size image, which is what a
        reader expects from a link.
      */
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
        return;
      }

      event.preventDefault();
      open(link);
    });

    closeButton.addEventListener("click", close);
    nextButton.addEventListener("click", next);
    prevButton.addEventListener("click", previous);

    /* Clicking the backdrop closes, but clicking the image itself does not. */
    lightbox.addEventListener("click", function (event) {
      if (event.target === lightbox || event.target.closest(".lightbox-figure") === null) {
        close();
      }
    });

    document.addEventListener("keydown", function (event) {
      if (lightbox.hidden) {
        return;
      }

      switch (event.key) {
        case "Escape":
          close();
          break;
        case "ArrowRight":
          next();
          break;
        case "ArrowLeft":
          previous();
          break;
        case "Tab":
          /*
            Focus is trapped inside the overlay while it is open. A dialog that
            lets focus wander behind it is disorienting for a keyboard user,
            who can end up interacting with content they cannot see.
          */
          trapFocus(event);
          break;
      }
    });

    function trapFocus(event) {
      const focusable = lightbox.querySelectorAll("button");
      if (focusable.length === 0) {
        return;
      }

      const first = focusable[0];
      const last = focusable[focusable.length - 1];

      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }

    /* Swipe gestures, so the gallery behaves the way a phone user expects. */
    let touchStartX = 0;

    lightbox.addEventListener(
      "touchstart",
      function (event) {
        touchStartX = event.changedTouches[0].screenX;
      },
      { passive: true }
    );

    lightbox.addEventListener(
      "touchend",
      function (event) {
        const distance = event.changedTouches[0].screenX - touchStartX;

        /*
          A threshold of 50 pixels distinguishes a deliberate swipe from the
          small horizontal drift in a tap or a vertical scroll.
        */
        if (Math.abs(distance) < 50) {
          return;
        }

        if (distance < 0) {
          next();
        } else {
          previous();
        }
      },
      { passive: true }
    );
  }

  /* ------------------------------------------------------------------ */
  /* Galleries                                                           */
  /* ------------------------------------------------------------------ */

  /*
    A gallery arrives from the server as a horizontally scrolling strip of
    slides with CSS scroll snapping, which is already a working carousel: it
    swipes on a phone, it scrolls with a trackpad, and each slide is a link to
    the full-size image. What is added here is the part CSS cannot do, which is
    arrows and dots that wrap around the ends.

    The strip is the source of truth for which slide is showing rather than a
    counter kept alongside it. A reader can swipe, and if the counter were
    authoritative the arrows would afterwards move relative to a position that
    is no longer where anybody is looking.
  */

  const reducedMotion = window.matchMedia
    ? window.matchMedia("(prefers-reduced-motion: reduce)")
    : null;

  function enhanceGalleries(scope) {
    (scope || document).querySelectorAll(".bs-gallery").forEach(setUpGallery);
  }

  function setUpGallery(figure) {
    /* Infinite scroll and search both re-run this over content that may
       already include enhanced galleries. */
    if (figure.dataset.carousel === "on") {
      return;
    }

    const track = figure.querySelector(".bs-gallery-track");
    if (!track) {
      return;
    }

    const slides = Array.from(track.querySelectorAll(".bs-gallery-slide"));
    if (slides.length < 2) {
      return;
    }

    figure.dataset.carousel = "on";

    const controls = document.createElement("div");
    controls.className = "bs-gallery-controls";

    const previousButton = arrow("‹", "Previous image", function () {
      go(current() - 1);
    });
    const nextButton = arrow("›", "Next image", function () {
      go(current() + 1);
    });

    const dots = document.createElement("div");
    dots.className = "bs-gallery-dots";

    const dotButtons = slides.map(function (slide, index) {
      const dot = document.createElement("button");
      dot.type = "button";
      dot.className = "bs-gallery-dot";
      dot.setAttribute("aria-label", "Image " + (index + 1) + " of " + slides.length);
      dot.addEventListener("click", function () {
        go(index);
      });
      dots.appendChild(dot);
      return dot;
    });

    /*
      A count in words for anyone who cannot see the dots. It is announced
      politely, so it waits for a pause rather than interrupting.
    */
    const status = document.createElement("p");
    status.className = "bs-gallery-status";
    status.setAttribute("aria-live", "polite");

    controls.appendChild(previousButton);
    controls.appendChild(dots);
    controls.appendChild(nextButton);

    /* Before the figcaption, so the caption stays the last thing in the
       figure where a reader expects to find it. */
    const caption = figure.querySelector("figcaption");
    if (caption) {
      figure.insertBefore(controls, caption);
      figure.insertBefore(status, caption);
    } else {
      figure.appendChild(controls);
      figure.appendChild(status);
    }

    function arrow(glyph, label, onClick) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "bs-gallery-arrow";
      button.textContent = glyph;
      button.setAttribute("aria-label", label);
      button.addEventListener("click", onClick);
      return button;
    }

    /*
      Which slide is showing, worked out from the scroll offset. The nearest
      slide wins rather than the first one past the left edge, so a strip left
      halfway between two by a flick settles on the one mostly in view.
    */
    function current() {
      let nearest = 0;
      let shortest = Infinity;

      slides.forEach(function (slide, index) {
        const distance = Math.abs(slide.offsetLeft - track.scrollLeft - track.offsetLeft);
        if (distance < shortest) {
          shortest = distance;
          nearest = index;
        }
      });

      return nearest;
    }

    function go(index) {
      const from = current();

      /* This is the wrap: past the last slide is the first, and before the
         first is the last. */
      const to = (index + slides.length) % slides.length;

      /*
        A wrap is jumped to rather than scrolled to. Sliding the whole strip
        back past ten photographs to reach the one after the last is a long
        animation that tells the reader nothing, and on a gallery of any size
        it reads as the page having lost its place.
      */
      const wrapped = Math.abs(to - from) > 1;
      const instant = wrapped || (reducedMotion && reducedMotion.matches);

      /*
        "instant" rather than "auto". They are not the same thing: "auto" means
        defer to the element's computed scroll-behavior, so on a container
        styled smooth it animates anyway and the jump this is asking for never
        happens.
      */
      track.scrollTo({
        left: slides[to].offsetLeft - track.offsetLeft,
        behavior: instant ? "instant" : "smooth",
      });

      /* Updated straight away rather than waiting for the scroll handler, so
         the dots respond on the click instead of after the animation. */
      mark(to);
    }

    function mark(index) {
      dotButtons.forEach(function (dot, at) {
        dot.classList.toggle("is-current", at === index);
        if (at === index) {
          dot.setAttribute("aria-current", "true");
        } else {
          dot.removeAttribute("aria-current");
        }
      });

      status.textContent = "Image " + (index + 1) + " of " + slides.length;
    }

    /*
      Following the strip while it is scrolled by hand. The work is deferred to
      an animation frame because a scroll fires far more often than the display
      refreshes, and recomputing distances on every one of those events is the
      classic way to make a touch gesture feel heavy.
    */
    let pending = false;

    track.addEventListener(
      "scroll",
      function () {
        if (pending) {
          return;
        }
        pending = true;
        window.requestAnimationFrame(function () {
          pending = false;
          mark(current());
        });
      },
      { passive: true }
    );

    /* Arrow keys while focus is anywhere in the strip, which is what somebody
       who has just tabbed onto a slide will try. */
    track.addEventListener("keydown", function (event) {
      if (event.key === "ArrowRight") {
        event.preventDefault();
        go(current() + 1);
      } else if (event.key === "ArrowLeft") {
        event.preventDefault();
        go(current() - 1);
      }
    });

    mark(0);
  }

  enhanceGalleries();

  /* ------------------------------------------------------------------ */
  /* Infinite scroll                                                     */
  /* ------------------------------------------------------------------ */

  /*
    The server renders a real <a rel="next"> for the following page. This turns
    it into an automatic append when it comes into view, and leaves it as a
    working link if anything here fails.
  */
  const postList = document.getElementById("post-list");

  if (postList && "IntersectionObserver" in window) {
    let loading = false;

    function currentLink() {
      const pagination = document.getElementById("pagination");
      return pagination ? pagination.querySelector("a[rel='next']") : null;
    }

    function loadNextPage() {
      const link = currentLink();
      if (!link || loading) {
        return;
      }

      loading = true;
      const pagination = link.parentElement;
      pagination.classList.add("is-loading");

      fetch(link.getAttribute("href"), {
        headers: { "X-Requested-With": "fetch" },
      })
        .then(function (response) {
          if (!response.ok) {
            throw new Error("Request failed with status " + response.status);
          }
          return response.text();
        })
        .then(function (html) {
          /*
            The response is a fragment of post summaries plus its own
            pagination link. Parsing it in a template element rather than
            assigning to innerHTML on a live node means nothing is evaluated
            during parsing and no partially built markup ever reaches the page.
          */
          const parsed = document.createElement("template");
          parsed.innerHTML = html;

          const appended = parsed.content.querySelectorAll(".post");
          appended.forEach(function (article) {
            article.classList.add("is-appended");
            postList.appendChild(article);
            /* After it is in the document, because the carousel measures
               offsets and a detached node has none. */
            enhanceGalleries(article);
          });

          /*
            Replace the old pagination with the one from the response, which
            carries the cursor for the page after this. Removing it entirely
            when the response has none is what ends the sequence.
          */
          const nextPagination = parsed.content.querySelector("#pagination");
          pagination.remove();

          if (nextPagination) {
            postList.parentElement.appendChild(nextPagination);
            observeSentinel();
          }
        })
        .catch(function () {
          /*
            On failure the pagination link is left exactly as it was, so the
            reader can click it and get the next page as an ordinary
            navigation. A failed enhancement should degrade to the behavior it
            was enhancing, not to a dead end.
          */
          pagination.classList.remove("is-loading");
        })
        .finally(function () {
          loading = false;
        });
    }

    /*
      rootMargin starts the fetch several hundred pixels before the link is
      actually visible, so the next posts are usually in place by the time the
      reader would have reached the bottom.
    */
    const observer = new IntersectionObserver(
      function (entries) {
        entries.forEach(function (entry) {
          if (entry.isIntersecting) {
            loadNextPage();
          }
        });
      },
      { rootMargin: "600px 0px" }
    );

    function observeSentinel() {
      const link = currentLink();
      if (link) {
        observer.observe(link);
      }
    }

    observeSentinel();
  }

  /* ------------------------------------------------------------------ */
  /* Search                                                              */
  /* ------------------------------------------------------------------ */

  /*
    The search form is a working GET form on its own. Submitting it reloads the
    page with filtered results, which is what a reader without JavaScript gets
    and what a shared result URL does. Everything below replaces that reload
    with an in-place swap so the reader never leaves the page they are reading.
  */
  const searchForm = document.getElementById("search-form");

  if (searchForm) {
    const field = document.getElementById("q");
    const details = document.getElementById("search");
    const modeInput = document.getElementById("search-mode");
    const modeButtons = document.querySelectorAll(".mode");

    /*
      Opening and closing the panel is the <details> element's own job, so
      there is no toggle handler here. The only thing worth adding is putting
      the cursor in the field when it opens, which saves a click and is what
      anyone who just reached for a search icon was about to do anyway.
    */
    if (details) {
      details.addEventListener("toggle", function () {
        if (details.open) {
          field.focus();
        }
      });
    }

    function currentMode() {
      return modeInput ? modeInput.value : "content";
    }

    function setMode(mode) {
      if (modeInput) {
        modeInput.value = mode;
      }
      modeButtons.forEach(function (button) {
        button.setAttribute(
          "aria-pressed",
          button.dataset.mode === mode ? "true" : "false"
        );
      });
    }

    setMode(currentMode());

    /*
      The mode buttons are submit buttons in the markup, so they work without
      JavaScript. Here they set the mode and run the search in place instead.
    */
    modeButtons.forEach(function (button) {
      button.addEventListener("click", function (event) {
        event.preventDefault();
        setMode(button.dataset.mode);
        runSearch();
      });
    });

    searchForm.addEventListener("submit", function (event) {
      event.preventDefault();
      runSearch();
    });

    /*
      Typing searches without waiting for a submit, debounced so a query is not
      fired for every keystroke. 250ms is long enough to collect a burst of
      typing and short enough to still feel like live filtering.
    */
    let debounce = null;

    field.addEventListener("input", function () {
      window.clearTimeout(debounce);
      debounce = window.setTimeout(runSearch, 250);
    });

    let searchToken = 0;

    function runSearch() {
      const query = field.value.trim();
      const mode = currentMode();

      /*
        Each request carries a token, and a response whose token is stale is
        discarded. Without this, a slow request for "as" can land after a fast
        one for "astronomy" and overwrite the correct results with older ones.
      */
      searchToken += 1;
      const token = searchToken;

      const url = query
        ? "/?q=" + encodeURIComponent(query) + "&mode=" + encodeURIComponent(mode)
        : "/";

      const main = document.getElementById("main");
      main.classList.add("is-loading");

      fetch(url, { headers: { "X-Requested-With": "fetch" } })
        .then(function (response) {
          if (!response.ok) {
            throw new Error("Request failed with status " + response.status);
          }
          return response.text();
        })
        .then(function (html) {
          if (token !== searchToken) {
            return; /* A newer search has already been issued. */
          }
          render(html, query, mode, url);
        })
        .catch(function () {
          /*
            Fall back to a full navigation. A failed enhancement should degrade
            to the behavior it was enhancing rather than to a dead end.
          */
          window.location.href = url;
        })
        .finally(function () {
          if (token === searchToken) {
            main.classList.remove("is-loading");
          }
        });
    }

    function render(html, query, mode, url) {
      const parsed = document.createElement("template");
      parsed.innerHTML = html;

      let list = document.getElementById("post-list");
      if (!list) {
        /*
          The previous search returned nothing, so there is no list to replace.
          Rebuilding it keeps the next search from having nowhere to put its
          results.
        */
        const main = document.getElementById("main");
        main.innerHTML = "";
        list = document.createElement("div");
        list.className = "post-list";
        list.id = "post-list";
        main.appendChild(list);
      }

      list.innerHTML = "";

      const posts = parsed.content.querySelectorAll(".post");
      posts.forEach(function (post) {
        post.classList.add("is-appended");
        list.appendChild(post);
        enhanceGalleries(post);
      });

      updateBanner(query, mode, posts.length);
      replacePagination(parsed);

      /*
        This only ever opens the panel, never closes it, and the asymmetry is
        deliberate. Assigning the condition directly would close the panel
        whenever the field happened to be empty, which meant switching between
        Tags and Content before typing anything slammed it shut under the
        reader's cursor. Closing is the reader's decision to make.
      */
      if (details && query !== "") {
        details.open = true;
      }

      /*
        The address bar is updated so the result can be copied, shared, and
        reloaded, and so the back button returns to the previous view. replace
        is not used, because a reader who searches and then presses back
        expects to return to the unfiltered timeline.
      */
      window.history.pushState({ query: query, mode: mode }, "", url);

      if (posts.length === 0) {
        showEmpty(list);
      }

      /* Bring the top of the results into view after a filter. */
      window.scrollTo({ top: 0, behavior: "smooth" });
    }

    function showEmpty(list) {
      const empty = document.createElement("div");
      empty.className = "empty";
      const message = document.createElement("p");
      message.textContent = "Nothing matched.";
      empty.appendChild(message);
      list.appendChild(empty);
    }

    function updateBanner(query, mode, count) {
      const existing = document.getElementById("results-banner");
      if (existing) {
        existing.remove();
      }

      if (!query) {
        return;
      }

      /*
        The banner is built with DOM calls rather than an HTML string, so the
        query is inserted as text and cannot be interpreted as markup. It comes
        from the reader's own keyboard, but building markup by concatenation is
        the habit worth not having.
      */
      const banner = document.createElement("div");
      banner.className = "results-banner";
      banner.id = "results-banner";

      const line = document.createElement("p");
      line.appendChild(
        document.createTextNode(
          count + " " + (count === 1 ? "result" : "results") + " for "
        )
      );

      const term = document.createElement("strong");
      term.textContent = query;
      line.appendChild(term);

      const where = document.createElement("span");
      where.className = "results-mode";
      where.textContent = " in " + mode;
      line.appendChild(where);

      const clear = document.createElement("a");
      clear.className = "clear-search";
      clear.href = "/";
      clear.textContent = "Clear";

      banner.appendChild(line);
      banner.appendChild(clear);

      const main = document.getElementById("main");
      main.insertBefore(banner, main.firstChild);
    }

    function replacePagination(parsed) {
      const existing = document.getElementById("pagination");
      if (existing) {
        existing.remove();
      }

      const next = parsed.content.querySelector("#pagination");
      if (next) {
        document.getElementById("main").appendChild(next);
      }
    }

    /*
      Restore the right view when the reader uses the back button. Without this
      the URL changes but the page does not, which is worse than not touching
      history at all.
    */
    window.addEventListener("popstate", function (event) {
      const state = event.state || {};
      field.value = state.query || "";
      setMode(state.mode || "content");
      runSearch();
    });
  }


  /* ------------------------------------------------------------------ */
  /* Settings: color pickers                                             */
  /* ------------------------------------------------------------------ */

  /*
    Each color setting is a native picker beside a text field holding the same
    value. The picker is the pleasant way to choose one; the text field is how
    an exact hex code gets pasted in from somewhere else, which a picker alone
    makes surprisingly awkward.

    Only the picker needs wiring: it writes into the text field, which is the
    input the form actually submits. Typing a hex code by hand does not update
    the swatch, which is a deliberate omission rather than an oversight, since
    reformatting a value while somebody is halfway through typing it is worse
    than the swatch briefly disagreeing.
  */
  document.querySelectorAll("input[type=color][data-syncs]").forEach(function (picker) {
    const target = document.getElementById(picker.dataset.syncs);
    if (!target) {
      return;
    }
    picker.addEventListener("input", function () {
      target.value = picker.value;
    });
  });


  /* ------------------------------------------------------------------ */
  /* Settings: social rows and the footer default                        */
  /* ------------------------------------------------------------------ */

  /*
    Both of these are conveniences on top of markup that already works. The
    form renders three spare social rows, so a link can be added without this,
    and the footer can be reset by clearing the field and saving. Neither is
    load bearing.
  */

  const addSocial = document.getElementById("add-social");
  const socialRows = document.getElementById("social-rows");

  if (addSocial && socialRows) {
    addSocial.addEventListener("click", function () {
      const last = socialRows.lastElementChild;
      if (!last) {
        return;
      }

      /*
        Cloning the last row rather than building one from a string means the
        options, labels, and names stay exactly in step with what the server
        rendered. A hand-written copy here would silently drift the moment a
        platform is added to the table.
      */
      const row = last.cloneNode(true);

      row.querySelectorAll("input").forEach(function (input) {
        input.value = "";
      });
      row.querySelectorAll("select").forEach(function (select) {
        select.selectedIndex = 0;
      });

      socialRows.appendChild(row);
      const first = row.querySelector("select");
      if (first) {
        first.focus();
      }
    });
  }

  const resetFooter = document.getElementById("reset-footer");

  if (resetFooter) {
    resetFooter.addEventListener("click", function () {
      const field = document.getElementById("footer_text");
      if (!field) {
        return;
      }
      /*
        The default is carried on the element by the server rather than
        duplicated here, so there is one place it is defined.
      */
      field.value = field.dataset.default || "";
      field.focus();
    });
  }


  /* ------------------------------------------------------------------ */
  /* Settings: live date format preview                                  */
  /* ------------------------------------------------------------------ */

  /*
    The format language expands each letter on its own, so "YYYY-MM-DD", which
    is what somebody arriving from almost any other date library will type, is
    accepted and renders as nonsense. Rejecting it would mean inventing a rule
    about repeated tokens that the language does not otherwise have, so instead
    the result is shown as it is typed and the mistake is obvious at once.

    Without this the field still works; the help text lists the tokens and the
    examples show what they produce.
  */
  const dateFormat = document.getElementById("date_format");

  if (dateFormat) {
    const preview = document.createElement("p");
    preview.className = "settings-note date-preview";
    dateFormat.insertAdjacentElement("afterend", preview);

    /* A single-digit day, so padded and unpadded forms differ visibly. */
    const sample = new Date(2026, 7, 6);

    const months = [
      "January", "February", "March", "April", "May", "June",
      "July", "August", "September", "October", "November", "December"
    ];
    const dividers = " -,|_:;./\\";

    function render(format) {
      let out = "";
      for (const character of format) {
        switch (character) {
          case "M": out += months[sample.getMonth()]; break;
          case "m": out += months[sample.getMonth()].slice(0, 3); break;
          case "D": out += String(sample.getDate()); break;
          case "d": out += String(sample.getDate()).padStart(2, "0"); break;
          case "Y": out += String(sample.getFullYear()); break;
          case "y": out += String(sample.getFullYear()).slice(-2); break;
          default:
            if (dividers.includes(character)) {
              out += character;
            }
        }
      }
      return out.trim();
    }

    function update() {
      const result = render(dateFormat.value);
      preview.textContent = result ? "Preview: " + result : "That format produces nothing.";
    }

    dateFormat.addEventListener("input", update);
    update();
  }

})();
