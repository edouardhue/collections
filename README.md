# Suivi de prise de vue de collections scientifiques

Ce programme est un robot qui compte les images présentes sur Wikimedia Commons d'une liste de spécimens de taxons, produit un rapport et met à jour une page arbitraire d'un projet Wikimedia.

Quand la liste de spécimens correspond au catalogue d'une collection scientifique, le rapport produit aide à choisir les spécimens à photographier en priorité, en indiquant le nombre d'images déjà présentes.

## Format de la liste

La liste de spécimens est un fichier CSV. Les colonnes doivent être les suivantes :

```
Nom scientifique du taxon,Nom vernaculaire,Type de naturalisation,Caractéristiques du spécimen,<Colonne ignorée>,<Colonne ignorée>,Numéro d'inventaire,Identifiant Wikidata du taxon,Nom de la catégorie Commons du spécimen
```

## Génération du rapport

Le rapport est généré à l'aide du [moteur de templating natif de Go](https://golang.org/pkg/text/template/). Le *dot* est un *range* contenant tous les spécimens, avec la structure suivante :
```go
type specimen struct {
	OriginalName        string // Nom scientifique provenant de la liste de spécimens
	VernacularName      string // Nom vernaculaire provenant de la même liste
	WikidataItemId      string // Identifiant de l'item Wikidata
	CommonsCategoryName string // Nom de la catégorie Commons du taxon (sans le préfixe Category:)
	FileCount           int    // Nombre de fichier dans cette catégorie
	SubCats             int    // Nombre de sous-catégories de cette catégorie
	SubCatsFileCounts   int    // Nombre cumulé de fichiers dans ces sous-catégories
	TotalFiles          int    // Total des fichiers de la catégorie et de ses sous-catégories
	Treatment           string // Concaténation du type de naturalisation et des caractéristiques du spécimen
	AccessionNumber     string // Numéro d'inventaire
	SpecimenCategory    string // Nom de la catégorie du spécimen
}
```

## Lancement

Le programme reconnaît les arguments suivants sur la ligne de commande. Tous sont obligatoires.

```
-f Emplacement du fichier de de la liste de spécimens.
-w URL de l'API du wiki où publier le rapport (exemple : https://fr.wikipedia.org/w/api.php pour Wikipédia en français).
-p Nom de la page à mettre à jour sur le wiki cible (exemple : Projet:Collections/Mammifères).
-s Numéro de la section à remplacer avec le rapport.
-t Emplacement du template à utiliser pour produire le rapport.
```

Le programme s'authentifie pour publier le rapport. Les identifiants doivent être placés dans des variables d'environnement.
```
BOT_LOGIN : nom d'utilisateur
BOT_PASSWORD : mot de passe
```