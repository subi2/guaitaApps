/* Guaita bridge — enganxa aquest <script> a qualsevol app Guaita.
   Funciona també si l'app s'obre sola fora del launcher (llavors dins() = false). */
(function (global) {
  'use strict';

  var dins = global.parent !== global;
  var onFitxersCB = null;
  var appsCB = null;
  var safataCB = null;
  var dragExtern = null; // fitxers d'un arrossegament Guaita en curs (xip o altra app)

  function envia(msg) {
    if (!dins) return;
    try { global.parent.postMessage(msg, '*'); } catch (e) { /* fora del launcher */ }
  }

  global.addEventListener('message', function (ev) {
    var m = ev.data;
    if (!m || typeof m.tipus !== 'string') return;
    if (m.tipus === 'guaita:fitxers') {
      if (onFitxersCB) onFitxersCB(m.fitxers || []);
      else dropSintetic(m.fitxers || []);
    } else if (m.tipus === 'guaita:apps' && appsCB) {
      var cb = appsCB; appsCB = null;
      cb(m.apps || []);
    } else if (m.tipus === 'guaita:safata' && safataCB) {
      var cbs = safataCB; safataCB = null;
      cbs(m.fitxers || []);
    } else if (m.tipus === 'guaita:drag-fitxers-inici') {
      dragExtern = (m.fitxers && m.fitxers.length) ? m.fitxers : null;
    } else if (m.tipus === 'guaita:drag-fitxers-fi') {
      dragExtern = null;
    }
  });

  // Simula un drop natiu perquè les apps que ja accepten fitxers arrossegats
  // des de l'Explorador els rebin sense conèixer el pont. L'esdeveniment es
  // dispara a l'element del centre de la finestra i puja per tots els
  // ancestres (wrap, body, document, window), on solen ser els listeners.
  function dropSintetic(fitxers, objectiuOpc) {
    if (!fitxers.length) return;
    try {
      var dt = new DataTransfer();
      for (var i = 0; i < fitxers.length; i++) dt.items.add(fitxers[i]);
      var objectiu = objectiuOpc || null;
      if (!objectiu) {
        try {
          if (document.elementFromPoint) objectiu = document.elementFromPoint(global.innerWidth / 2, global.innerHeight / 2);
        } catch (e) { /* sense layout */ }
      }
      if (!objectiu) objectiu = document.body || document.documentElement;
      var over = new DragEvent('dragover', { bubbles: true, cancelable: true, dataTransfer: dt });
      var drop = new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: dt });
      over.__guaita = true; drop.__guaita = true; // evita que l'interceptor els torni a capturar
      objectiu.dispatchEvent(over);
      objectiu.dispatchEvent(drop);
    } catch (e) { /* navegador sense DataTransfer() */ }
  }

  // Interceptor: durant un arrossegament Guaita (xip de la Safata o fila d'una
  // altra app), si el drop natiu arriba SENSE fitxers (Chromium no sempre
  // transporta File afegits per script entre documents), el substituïm per un
  // drop sintètic amb els fitxers rebuts pel canal de missatges, al mateix punt.
  if (dins) {
    global.addEventListener('dragover', function (ev) {
      if (dragExtern && !ev.__guaita) ev.preventDefault();
    }, true);
    global.addEventListener('drop', function (ev) {
      if (!dragExtern || ev.__guaita) return;
      if (ev.dataTransfer && ev.dataTransfer.files && ev.dataTransfer.files.length) {
        dragExtern = null; // el canal natiu ha funcionat: deixa passar el drop real
        return;
      }
      ev.preventDefault();
      ev.stopPropagation();
      var fitxers = dragExtern; dragExtern = null;
      dropSintetic(fitxers, ev.target);
    }, true);
  }

  // Si l'app no registra onFitxers, anuncia "llest" igualment un cop carregada:
  // així la cua del launcher es buida i el drop sintètic pot actuar.
  if (dins) {
    global.addEventListener('load', function () {
      setTimeout(function () {
        if (!onFitxersCB) envia({ tipus: 'guaita:llest' });
      }, 400);
    });
  }

  var Guaita = {
    // true si l'app corre dins del launcher Guaita apps.
    dins: function () { return dins; },

    // Rep fitxers enviats des d'una altra app. Cridar-ho un cop, en carregar.
    // El callback rep un array de File.
    onFitxers: function (cb) {
      onFitxersCB = cb;
      envia({ tipus: 'guaita:llest' });
    },

    // Demana la llista d'apps disponibles (per muntar un menú "Obre amb...").
    // El callback rep [{file, nom}].
    apps: function (cb) {
      if (!dins) { cb([]); return; }
      appsCB = cb;
      envia({ tipus: 'guaita:apps?' });
    },

    // Obre els fitxers indicats amb una altra app (file = nom del seu HTML).
    obreAmb: function (appFile, fitxers) {
      envia({ tipus: 'guaita:obre-amb', app: appFile, fitxers: Array.from(fitxers) });
    },

    // Diposita fitxers a la safata del launcher, visible a la barra lateral.
    aSafata: function (fitxers) {
      envia({ tipus: 'guaita:a-safata', fitxers: Array.from(fitxers) });
    },

    // Demana el contingut actual de la safata. El callback rep un array de File.
    safata: function (cb) {
      if (!dins) { cb([]); return; }
      safataCB = cb;
      envia({ tipus: 'guaita:safata?' });
    },

    // Fa que un element sigui arrossegable cap a la barra lateral del launcher.
    // getFitxers() ha de retornar els File a passar.
    ferArrossegable: function (el, getFitxers) {
      el.draggable = true;
      el.addEventListener('dragstart', function (ev) {
        var f = getFitxers();
        if (!f || !f.length) { ev.preventDefault(); return; }
        // Adjunta els File al dataTransfer natiu: l'app destí els rep com si
        // vinguessin de l'Explorador, encara que no conegui el pont Guaita.
        try {
          for (var i = 0; i < f.length; i++) ev.dataTransfer.items.add(f[i]);
        } catch (e) { /* navegador sense suport */ }
        ev.dataTransfer.setData('text/plain', f[0].name);
        ev.dataTransfer.effectAllowed = 'copy';
        envia({ tipus: 'guaita:drag-inici', fitxers: Array.from(f) });
      });
      el.addEventListener('dragend', function () {
        envia({ tipus: 'guaita:drag-fi' });
      });
    }
  };

  global.Guaita = Guaita;
})(window);
