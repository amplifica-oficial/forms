const API_BASE = window.location.hostname == 'localhost' ? 'http://localhost:5000' : 'https://forms.cert.ninja/api';
const REDIRECT_TO = window.location.hostname == 'localhost' ? '' : 'https://cert.ninja';

document.addEventListener('DOMContentLoaded', () => { 
    onLoad().catch(err => {
        PageError(content, getErrorMessage(err) || err);

        if (err.message.includes('no form') && REDIRECT_TO) {
            window.location = REDIRECT_TO
        }
    })
})

async function onLoad() {
    /** @type HTMLDivElement */
    let content = document.getElementById('content');

    /** @type string | null */
    let loginToken = null;

    /** @type string | null */
    let email = null;

    const url = new URL(window.location.href);

    let slug = url.pathname.replace(/^\//, '');
    if (slug.includes('/')) {
        throw new Error("form can't contain /")
    }

    if (url.searchParams.get('state')) {
        const login = await execute('POST', '/CompleteLogin?' + url.searchParams);

        // After login
        loginToken = login.Token;

        const claims = getClaims(loginToken);
        slug = claims.form;
        email = claims.email;

        window.history.replaceState(null, '', url.origin + '/' + slug);
    }

    if (slug) {
        const form = await execute('GET', withQuery('/ClientGetFormInfo', { slug }));

        if (form.LoginUrl) {
            PageFormLogin(content, slug, email, form.LoginUrl, loginToken);

        } else {
            PageFormNoLogin(content, slug);
        }

    } else {
        throw new Error('no form');
    }
}

/** @param {HTMLElement} target */
function PageError(target, err) {
    target.innerHTML = `
        <div class="card error-box">
            <p>${err}</p>
        </div>`;
}

/** @param {HTMLElement} target */
function PageSubmitted(target, submit) {
    if (submit.AlreadyAnswered) {
        target.innerHTML = `
            <div class="card text-center">
                <h2 class="text-foreground">Formulário já respondido</h2>
                <p class="text-muted-foreground">Você já registrou uma resposta para este formulário.</p>
            </div>`;
    } else {
        const date = new Date(submit.CreatedAt);
        target.innerHTML = `
            <div class="card text-center">
                <div class="icon-wrapper">
                    <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"></polyline></svg>
                </div>
                <h2 class="text-foreground">Obrigado!</h2>
                <p class="text-muted-foreground">Resposta registrada com sucesso em<br>${date.toLocaleString()}</p>
            </div>`;
    }
}

/** @param {HTMLElement} target */
function PageFormLogin(target, slug, email, loginUrl, token) {
    target.innerHTML =  `
    <div class="card">
        <form id="form-${slug}" class="form-layout">
            ${email ?
                `<label>
                    E-mail
                    <input type="email" id="email" disabled value="${email}" class="input-field"/>
                </label>` : ''}

            <a href="${loginUrl}" class="btn-outline">
                <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="margin-right: 8px;"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"></path></svg>
                ${email ? 'Trocar de conta' : 'Login com o Google'}
            </a>

            <label>
                Nome
                <input type="text" id="name" placeholder="Seu nome completo" class="input-field" ${email ? '' : 'disabled=""'} />
            </label>

            <div id='error-target' class="error-box" style="display: none;"></div>

            <input type="submit" value="Enviar" class="btn-primary" ${email ? '' : 'disabled'} />
        </form>
    </div>`;

    document.getElementById(`form-${slug}`).addEventListener('submit', async (ev) => {
        ev.preventDefault();

        /** @type HTMLInputElement */
        const nameInput = document.getElementById('name')

        const name = nameInput.value;

        try {
            await submitData(target, slug, null, name, token);
        } catch (err) {
            const errorMessage = getErrorMessage(err) || err.message;
            const errorTarget = document.getElementById('error-target');
            errorTarget.innerText = errorMessage;
            errorTarget.style.display = '';
        }
    })
}


/** @param {HTMLElement} target */
function PageFormNoLogin(target, slug) {
    target.innerHTML = `
    <div class="card">
        <form id="form-${slug}" class="form-layout">
            <label>
                E-mail
                <input type="email" id="email" placeholder="seu@email.com" class="input-field" />
            </label>

            <label>
                Nome
                <input type="text" id="name" placeholder="Seu nome completo" class="input-field" />
            </label>

            <div id='error-target' class="error-box" style="display: none;"></div>

            <input type="submit" value="Enviar" class="btn-primary" />
        </form>
    </div>`

    document.getElementById(`form-${slug}`).addEventListener('submit', async (ev) => {
        ev.preventDefault();

        /** @type HTMLInputElement */
        const emailInput = document.getElementById('email')

        /** @type HTMLInputElement */
        const nameInput = document.getElementById('name')

        const email = emailInput.value;
        const name = nameInput.value;

        try {
            await submitData(target, slug, email, name, null);
        } catch (err) {
            const errorMessage = getErrorMessage(err) || err.message;
            const errorTarget = document.getElementById('error-target');
            errorTarget.innerText = errorMessage;
            errorTarget.style.display = '';
        }
    })
}

function getErrorMessage(err) {
    if (err.message.includes('before-open')) {
        return 'Esse formulário ainda não abriu';
    }
    if (err.message.includes('after-close')) {
        return "Esse formulário já fechou";
    }
    if (err.message.includes('bad-domain')) {
        return "Esse formulário precisa de uma conta de um domínio específico";
    }
    return null;
}

/**
 * @param {HTMLElement} target
 * @param {HTMLElement} errorTarget
 * @returns 'before-open' | 'after-close' | 'bad-domain' | null
 */
async function submitData(target, slug, email, name, token) {
    const submit = await execute('POST', '/CreateResponse', {
        slug,
        email,
        name,
        token,
    });

    PageSubmitted(target, submit);
}

/**
 * @param {'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'} method
 * @param {string} path
 * @param {any} body
 * @returns any
 */
async function execute(method, path, body = null) {
    console.log(`run [${method}] ${path}`)

    const headers = {
        'Content-Type': 'application/json; charset=utf-8',
    };

    const res = await fetch(API_BASE + path, {
        method,
        headers,
        body: body != null ? JSON.stringify(body) : null,
    })

    if (!res.ok) {
        let err;
        try {
            err = (await res.json())?.error;
        } catch { }

        throw new Error(`[${method}] ${path}: ${err || `${res.status} ${res.statusText}`}`);
    }

    return await res.json()
}

/**
 * @param {string} path
 * @param {Record<string, any>} params
 * @returns string
 */
function withQuery(path, params = null) {
    if (!params) return path;

    let searchParams = new URLSearchParams();

    if (path.includes('?')) {
        let params = '';
        [path, params] = path.split('?', 2);
        searchParams = new URLSearchParams(params);
    }

    for (const key in params) {
        if (params[key] !== null && typeof params[key] !== 'undefined') {
            searchParams.set(key, params[key]);
        } else {
            searchParams.delete(key)
        }
    }

    return path + '?' + searchParams;
}

/** * @param {string} token
 * @return {{ form: string, email: string }}
 */
function getClaims(token) {
    /** @type string */
    let payload = token.split('.')[1];
    payload = window.atob(payload)
    const data = JSON.parse(payload);

    return {
        form: data.aud[0],
        email: data.sub,
    }
}
