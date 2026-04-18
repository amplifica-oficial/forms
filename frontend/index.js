
/** @type HTMLDivElement */
let content;

let formSlug = '';
let token = '';

const templateError = `
<div>ERROR</div>
`;

function FormLogin(loggedInAs, loginUrl) {
	s = `<form id="formData">`;
	if (loggedInAs) {
		s += `<label>E-mail: <input type="email" id="email" disabled value="${loggedInAs}"/></label>`;
	} else {
		s += `<a href="${loginUrl}">login</a>`;
	}
	s += `<br>`;
	s += `<label>Name: <input type="text" id="name"  /></label>`;
	s += `<input type="submit" value="Submit" ${loggedInAs ? '' : 'disabled'} />`;
	s += `</form>`;
	return s
}

function Submitted(token) {
	return `<div>ok, your token is: ${token}</div>`;
}

function FormNoLogin() {
	return `<form id="formData">
		<label>
			E-mail:
			<input type="email" id="email" />
		</label>

		<br>

		<label>
			Name:
			<input type="text" id="name"  />
		</label>

		<input type="submit" value="Submit" />
	</form>`
}

/** @param {SubmitEvent} ev */
async function onFormNoLoginSubmit(ev) {
	ev.preventDefault();

	/** @type HTMLInputElement */
	const emailInput = document.getElementById('email')

	/** @type HTMLInputElement */
	const nameInput = document.getElementById('name')

	const email = emailInput.value;
	const name = nameInput.value;

	const token = await execute('POST', '/CreateResponse', {
		slug: formSlug,
		email,
		name,
	});

	content.innerHTML = Submitted(JSON.stringify(token));
}

const API_BASE = 'http://localhost:5000';

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

	//if (this.auth?.loggedIn) {
		//headers['Authorization'] = `Bearer ${this.auth.token}`;
	//}

	const res = await fetch(API_BASE + path, {
		method,
		headers,
		body: body != null ? JSON.stringify(body) : null,
	})

	if (!res.ok) {
		let err;
		try {
			err = (await res.json())?.message;
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

document.addEventListener('DOMContentLoaded', async () => {
	content = document.getElementById('content');

	const url = new URL(window.location.href);
	formSlug = url.pathname.replace(/^\//, '');
	if (formSlug.includes('/')) {
		content.innerHTML = templateError;
		return;
	}

	if (url.searchParams.get('state')) {
		const login = await execute('POST', '/CompleteLogin?' + url.searchParams).catch(() => {
			//window.location = 'https://cert.ninja';
		})

		// After login
		formSlug = login.Slug;
		token = login.Token;
		window.history.replaceState(null, '', url.origin + '/' + formSlug);
	}

	if (formSlug) {
		console.log(token)
		const form = await execute('GET', withQuery('/ClientGetFormInfo', { 'slug': formSlug })).catch(() => {
			//window.location = 'https://cert.ninja';
		})

		if (form.LoginUrl) {
			content.innerHTML = FormLogin(null, form.LoginUrl);
			return;
		} else {
			content.innerHTML = FormNoLogin();
			document.getElementById('formData').addEventListener('submit', onFormNoLoginSubmit);
			return;
		}
	} 

	//window.location = 'https://cert.ninja';
})

